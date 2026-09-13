package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

const tokenEstimate = 42

// counter adds the optional provider.TokenCounter capability to idle.
type counter struct{ idle }

func (counter) CountTokens(context.Context, provider.Request) (int64, error) {
	return tokenEstimate, nil
}

// countingSession serves a session whose provider can measure a request.
func countingSession(t *testing.T) *testSession {
	t.Helper()
	ctx := context.Background()
	var session *harness.Session
	service, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: config(), Authorize: httpapi.BearerToken("test-token"), Factory: func(ctx context.Context, c harness.Config) (*harness.Session, error) {
		var err error
		session, err = harness.New(ctx, c, harness.Dependencies{Provider: counter{}})
		return session, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(t, service, "POST", "/sessions", httpapi.CreateRequest{}); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	var _ http.Handler = service
	return &testSession{Session: session, http: service}
}

func agentInfo(t *testing.T, body []byte) conversation.AgentInfo {
	t.Helper()
	var info conversation.AgentInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// The pause/resume/stop routes forward to the session control surface and
// return the resulting agent snapshot as the response body.
func TestHTTPAgentControlRoutes(t *testing.T) {
	_, s := recoverySession(t, true)
	worker := createHTTPWorker(t, s)
	base := "/sessions/" + s.ID() + "/agents/" + string(worker)

	w := request(t, s.http, "POST", base+"/pause", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	paused := agentInfo(t, w.Body.Bytes())
	if paused.ID != worker || paused.State != agent.PauseRequested {
		t.Fatal("pause route did not request a halt", paused)
	}

	if w = request(t, s.http, "POST", base+"/resume", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	resumed := agentInfo(t, w.Body.Bytes())
	if resumed.State != agent.Running || resumed.StateRevision <= paused.StateRevision {
		t.Fatal("resume route did not restart the agent", resumed)
	}

	if w = request(t, s.http, "POST", base+"/stop", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	stopped := agentInfo(t, w.Body.Bytes())
	if stopped.State != agent.StopRequested && !stopped.State.Terminal() {
		t.Fatal("stop route left the agent runnable", stopped)
	}
}

// Control of an unknown agent is a 404, and an unrecognized action under a real
// agent is a 404 rather than a silent success.
func TestHTTPAgentControlRejectsUnknownAgentAndAction(t *testing.T) {
	_, s := recoverySession(t, true)
	worker := createHTTPWorker(t, s)
	base := "/sessions/" + s.ID() + "/agents/"
	cases := []struct {
		method, path string
		status       int
	}{
		{"POST", base + "missing/pause", 404},
		{"POST", base + "missing/resume", 404},
		{"POST", base + "missing/stop", 404},
		{"POST", base + string(worker) + "/detonate", 404},
		{"GET", base + string(worker) + "/pause", 404},
		{"POST", base + string(worker) + "/pause/twice", 404},
	}
	for _, tc := range cases {
		if w := request(t, s.http, tc.method, tc.path, nil); w.Code != tc.status {
			t.Fatal(tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// The token-count route decodes a revision from the body, rejects a malformed
// one, and reports a provider without the optional counting capability as
// unimplemented rather than as a failure.
func TestHTTPAgentTokenCountRoute(t *testing.T) {
	_, s := recoverySession(t, true)
	worker := createHTTPWorker(t, s)
	base := "/sessions/" + s.ID() + "/agents/" + string(worker) + "/tokens"

	// The idle provider does not implement provider.TokenCounter.
	if w := request(t, s.http, "POST", base, map[string]any{"revision": 0}); w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, s.http, "POST", base, map[string]any{"unexpected": true}); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}

	counting := countingSession(t)
	worker = createHTTPWorker(t, counting)
	w := request(t, counting.http, "POST", "/sessions/"+counting.ID()+"/agents/"+string(worker)+"/tokens", map[string]any{"revision": 1})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var counted struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &counted); err != nil {
		t.Fatal(err)
	}
	if counted.Count != tokenEstimate {
		t.Fatal("route did not return the provider count", counted.Count)
	}
}

// A receipt is addressable by message id; an unsent id is reported as missing
// rather than as a zero-valued receipt.
func TestHTTPReceiptRoute(t *testing.T) {
	_, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()

	w := request(t, s.http, "POST", base+"/messages", httpapi.SendRequest{To: s.Root(), Content: "hello"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var sent message.Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	w = request(t, s.http, "GET", base+"/receipts/"+string(sent.MessageID), nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got message.Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MessageID != sent.MessageID || got.Recipient != s.Root() {
		t.Fatal("receipt does not describe the sent message", got)
	}
	if w := request(t, s.http, "GET", base+"/receipts/never-sent", nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
