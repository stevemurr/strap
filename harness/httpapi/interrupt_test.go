package httpapi_test

import (
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
)

func TestHTTPInterruptKeepsSessionOpenForNextMessage(t *testing.T) {
	s := countingSession(t)
	base := "/sessions/" + s.ID()
	if w := request(t, s.http, "POST", base+"/interrupt", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.State() != harness.Open || s.Agents()[0].State != agent.Interrupted {
		t.Fatal("interrupt closed or failed to hold session")
	}
	if w := request(t, s.http, "POST", base+"/agents/"+string(s.Root())+"/resume", nil); w.Code != 409 {
		t.Fatal("resume bypassed interruption", w.Code, w.Body.String())
	}
	if w := request(t, s.http, "POST", base+"/messages", httpapi.SendRequest{To: s.Root(), Content: "new task"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.State() != harness.Open {
		t.Fatal("new message replaced session")
	}
}
