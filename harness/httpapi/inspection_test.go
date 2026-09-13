package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/inspection"
)

func TestSessionTraceRoutesUseIndependentInspectionAndAuthorization(t *testing.T) {
	ctx, s := recoverySession(t, true)
	reader, err := s.Trace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	view, err := reader.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := view.ListAgents(ctx, inspection.AgentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/sessions/%s/trace/agents?through=%d", s.ID(), view.Through().Sequence)
	w := request(t, s.http, "GET", path, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got inspection.AgentPage
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatal(string(a), string(b))
	}
	unauth := httptest.NewRecorder()
	s.http.ServeHTTP(unauth, httptest.NewRequest("GET", path, nil))
	if unauth.Code != 403 {
		t.Fatal(unauth.Code)
	}
	if err = reader.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Send(s.Root(), "reader closure leaves execution usable"); err != nil {
		t.Fatal(err)
	}
}
