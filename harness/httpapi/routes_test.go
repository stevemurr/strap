package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/work"
)

// The session collection lists what the service owns and creates new members;
// anything else on that path is a method error rather than a silent no-op.
func TestHTTPSessionCollectionRoutes(t *testing.T) {
	_, s := recoverySession(t, true)
	w := request(t, s.http, "GET", "/sessions", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ids []string
	if err := json.Unmarshal(w.Body.Bytes(), &ids); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != s.ID() {
		t.Fatal("session listing did not name the live session", ids)
	}
	view := request(t, s.http, "GET", "/sessions/"+s.ID(), nil)
	if view.Code != 200 {
		t.Fatal(view.Code, view.Body.String())
	}
	var got httpapi.SessionView
	if err := json.Unmarshal(view.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Root != s.Root() {
		t.Fatal("session view named a different root", got)
	}
	cases := []struct {
		method, path string
		status       int
	}{
		{"DELETE", "/sessions", 405},
		{"POST", "/sessions/" + s.ID(), 405},
		{"GET", "/sessions/missing", 404},
		{"GET", "/elsewhere", 404},
		{"GET", "/sessions/" + s.ID() + "/nowhere", 404},
		{"POST", "/sessions/" + s.ID() + "/nowhere", 404},
		{"GET", "/sessions/" + s.ID() + "/work/a/b/c", 404},
	}
	for _, tc := range cases {
		if w := request(t, s.http, tc.method, tc.path, nil); w.Code != tc.status {
			t.Fatal(tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// Host-side session operations are reachable over HTTP and observable in the
// session's own state.
func TestHTTPSessionHostOperations(t *testing.T) {
	_, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()

	if w := request(t, s.http, "GET", base+"/agents", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, s.http, "POST", base+"/logs", conversation.DiagnosticEvent{Level: "info", Message: "noted"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// An unknown level is refused. It surfaces as 500 because the session's
	// validation error is not one of the classified request errors.
	if w := request(t, s.http, "POST", base+"/logs", conversation.DiagnosticEvent{Level: "shouting", Message: "noted"}); w.Code < 400 {
		t.Fatal("accepted an unknown diagnostic level", w.Code, w.Body.String())
	}
	if w := request(t, s.http, "POST", base+"/flush", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, s.http, "POST", base+"/dispose", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if state := s.State(); state != harness.Disposed {
		t.Fatal("dispose route left the session usable", state)
	}
	// History is gone after disposal, and says so rather than returning nothing.
	if w := request(t, s.http, "GET", base+"/events", nil); w.Code != 410 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Event queries are validated before any read, and a cursor beyond the log is
// refused rather than silently clamped.
func TestHTTPEventQueryValidation(t *testing.T) {
	_, s := recoverySession(t, true)
	base := "/sessions/" + s.ID() + "/events"
	for _, path := range []string{
		base + "?after=nope",
		base + "?limit=nope",
		base + "?max_bytes=nope",
		base + "?max_bytes=0",
		base + "?max_bytes=9999999999",
		base + "?limit=-1",
	} {
		if w := request(t, s.http, "GET", path, nil); w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if w := request(t, s.http, "GET", base+"?limit=10&max_bytes=65536", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// A request body must be exactly one JSON value; trailing content is rejected
// rather than ignored.
func TestHTTPRequestBodyMustBeOneJSONValue(t *testing.T) {
	_, s := recoverySession(t, true)
	r := httptest.NewRequest("POST", "/sessions/"+s.ID()+"/messages", strings.NewReader(`{"to":"root","content":"a"}{"to":"root","content":"b"}`))
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.http.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "exactly one JSON value") {
		t.Fatal(w.Code, w.Body.String())
	}
	// An idempotency key would imply automatic retry, which the API never does.
	r = httptest.NewRequest("POST", "/sessions/"+s.ID()+"/messages", bytes.NewReader([]byte(`{"to":"root","content":"a"}`)))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Idempotency-Key", "key")
	w = httptest.NewRecorder()
	s.http.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// An agent transcript is a paged read whose bounds are validated with the rest
// of the query.
func TestHTTPAgentTranscriptQuery(t *testing.T) {
	ctx, s := recoverySession(t, true)
	base := "/sessions/" + s.ID() + "/agents/" + string(s.Root())

	// Drive one turn so the root has a transcript to page through.
	if w := request(t, s.http, "POST", "/sessions/"+s.ID()+"/messages", httpapi.SendRequest{To: s.Root(), Content: "hello"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(wait)
		if err != nil {
			t.Fatal("the root never finished its turn", err)
		}
		if c, ok := e.(conversation.AgentStateChanged); ok && c.Agent == s.Root() && c.State == agent.Idle {
			break
		}
	}

	w := request(t, s.http, "GET", base+"?transcript=true", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var full harness.AgentInspection
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full.Transcript == nil || len(full.Transcript.Entries) == 0 {
		t.Fatal("a completed turn produced no transcript", w.Body.String())
	}
	// Paging back from the earliest entry returns nothing more.
	earliest := full.Transcript.Entries[0].Position
	paged := request(t, s.http, "GET", fmt.Sprintf("%s?transcript=true&before=%d&limit=5", base, earliest), nil)
	if paged.Code != 200 {
		t.Fatal(paged.Code, paged.Body.String())
	}
	var head harness.AgentInspection
	if err := json.Unmarshal(paged.Body.Bytes(), &head); err != nil {
		t.Fatal(err)
	}
	for _, entry := range head.Transcript.Entries {
		if entry.Position >= earliest {
			t.Fatal("paging returned an entry at or after its bound", entry.Position, earliest)
		}
	}
	for _, path := range []string{base + "?transcript=true&before=nope", base + "?transcript=true&limit=nope"} {
		if w := request(t, s.http, "GET", path, nil); w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	// A cursor past the end of the log is refused rather than clamped.
	if w := request(t, s.http, "GET", base+"?transcript=true&before=999999", nil); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Work artifacts are addressable by id, and an unknown id is a 404.
func TestHTTPWorkArtifactRoutes(t *testing.T) {
	_, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()
	worker := createHTTPWorker(t, s)
	w := request(t, s.http, "POST", base+"/work/assign", httpapi.WorkRequest[work.AssignmentRequest]{Actor: s.Root(), Request: work.AssignmentRequest{Kind: work.Implementation, Assignee: worker, Task: "task"}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var item work.Work
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	submitted := request(t, s.http, "POST", base+"/work/submit", httpapi.WorkRequest[work.SubmitRequest]{Actor: worker, Request: work.SubmitRequest{WorkTarget: work.WorkTarget{ID: item.ID, ExpectedRevision: item.Revision}, Summary: "done"}})
	if submitted.Code != 200 {
		t.Fatal(submitted.Code, submitted.Body.String())
	}
	var event work.Event
	if err := json.Unmarshal(submitted.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	got := request(t, s.http, "GET", base+"/submissions/"+string(event.SubmissionID)+"?actor="+string(s.Root()), nil)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var submission work.Submission
	if err := json.Unmarshal(got.Body.Bytes(), &submission); err != nil {
		t.Fatal(err)
	}
	if submission.ID != event.SubmissionID {
		t.Fatal("submission route returned a different artifact", submission)
	}
	for _, path := range []string{"/submissions/missing", "/plans/missing", "/audits/missing", "/work/missing"} {
		if w := request(t, s.http, "GET", base+path+"?actor="+string(s.Root()), nil); w.Code != 404 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}

// Service construction validates its own options rather than failing later on
// the first request.
func TestServiceOptionsValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := httpapi.New(ctx, httpapi.Options{MaxRequests: -1, Authorize: httpapi.BearerToken("t")}); err == nil {
		t.Fatal("accepted a negative request limit")
	}
	if _, err := httpapi.New(ctx, httpapi.Options{}); err == nil {
		t.Fatal("accepted a service with no authorization callback")
	}
	// A factory that returns no session and no error is a programming error, not
	// a usable session.
	service, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: config(), Authorize: httpapi.BearerToken("t"), Factory: func(context.Context, harness.Config) (*harness.Session, error) {
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(ctx)
	r := httptest.NewRequest("POST", "/sessions", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	service.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A factory that fails is reported to the caller.
	failing, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: config(), Authorize: httpapi.BearerToken("t"), Factory: func(context.Context, harness.Config) (*harness.Session, error) {
		return nil, errors.New("no capacity")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer failing.Close(ctx)
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/sessions", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer t")
	failing.ServeHTTP(w, r)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "no capacity") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A malformed creation body never reaches the factory.
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/sessions", strings.NewReader(`{"config":`))
	r.Header.Set("Authorization", "Bearer t")
	failing.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The service admits a bounded number of concurrent requests and sheds the rest
// rather than queueing them behind a slow one.
func TestServiceShedsLoadBeyondItsRequestLimit(t *testing.T) {
	ctx := context.Background()
	holding, release := make(chan struct{}), make(chan struct{})
	service, err := httpapi.New(ctx, httpapi.Options{MaxRequests: 1, DefaultConfig: config(), Authorize: func(*http.Request, httpapi.Capability, string) error {
		select {
		case holding <- struct{}{}:
			<-release // The first request parks here, holding the only slot.
		default:
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(ctx)

	occupied := make(chan struct{})
	go func() {
		defer close(occupied)
		service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/sessions", nil))
	}()
	<-holding
	shed := httptest.NewRecorder()
	service.ServeHTTP(shed, httptest.NewRequest("GET", "/sessions", nil))
	if shed.Code != 429 {
		t.Fatal("service did not shed load while its only slot was held", shed.Code, shed.Body.String())
	}
	close(release)
	<-occupied
	// The slot is returned once the first request completes.
	if w := request(t, service, "GET", "/sessions", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Authorization is checked per capability, so a token that cannot dispose is
// still allowed to read.
func TestServiceAuthorizesPerCapability(t *testing.T) {
	ctx := context.Background()
	var session *harness.Session
	seen := map[httpapi.Capability]bool{}
	service, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: config(), Factory: func(ctx context.Context, c harness.Config) (*harness.Session, error) {
		var e error
		session, e = harness.New(ctx, c, harness.Dependencies{Provider: idle{}})
		return session, e
	}, Authorize: func(_ *http.Request, c httpapi.Capability, _ string) error {
		seen[c] = true
		if c == httpapi.Dispose {
			return errors.New("disposal is not permitted")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(ctx)
	serve := func(method, path string) int {
		w := httptest.NewRecorder()
		service.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		return w.Code
	}
	if code := serve("POST", "/sessions"); code != 201 {
		t.Fatal(code)
	}
	base := "/sessions/" + session.ID()
	if code := serve("GET", base); code != 200 {
		t.Fatal(code)
	}
	if code := serve("POST", base+"/dispose"); code != 403 {
		t.Fatal("dispose was not refused", code)
	}
	if code := serve("POST", base+"/agents/"+string(session.Root())+"/tokens"); code != 501 {
		t.Fatal(code)
	}
	// Sending a message is an ordinary command.
	w := httptest.NewRecorder()
	service.ServeHTTP(w, httptest.NewRequest("POST", base+"/messages", strings.NewReader(`{"to":"`+string(session.Root())+`","content":"hi"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, c := range []httpapi.Capability{httpapi.Create, httpapi.Read, httpapi.Dispose, httpapi.Measure, httpapi.Command} {
		if !seen[c] {
			t.Fatal("capability was never checked:", c)
		}
	}
}

// An empty bearer token authorizes nothing, even a request that presents it.
func TestBearerTokenRejectsEmptyAndMismatchedTokens(t *testing.T) {
	empty := httpapi.BearerToken("")
	if err := empty(httptest.NewRequest("GET", "/sessions", nil), httpapi.Read, ""); err == nil {
		t.Fatal("an empty token authorized an anonymous request")
	}
	r := httptest.NewRequest("GET", "/sessions", nil)
	r.Header.Set("Authorization", "Bearer ")
	if err := empty(r, httpapi.Read, ""); err == nil {
		t.Fatal("an empty token authorized a request presenting it")
	}
	authorize := httpapi.BearerToken("secret")
	r = httptest.NewRequest("GET", "/sessions", nil)
	r.Header.Set("Authorization", "Token secret")
	if err := authorize(r, httpapi.Read, ""); err == nil {
		t.Fatal("accepted a non-bearer authorization scheme")
	}
	r.Header.Set("Authorization", "Bearer secret")
	if err := authorize(r, httpapi.Read, ""); err != nil {
		t.Fatal(err)
	}
}
