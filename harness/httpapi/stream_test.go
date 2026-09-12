package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
)

func openStream(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	r, err := http.NewRequest("GET", server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer test-token")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		t.Fatal(resp.Status)
	}
	return resp
}
func TestHTTPStreamDisconnectAndReconnectPreserveExecution(t *testing.T) {
	_, s := recoverySession(t, true)
	server := httptest.NewServer(s.http)
	defer server.Close()
	path := "/sessions/" + s.ID() + "/events/stream"
	response := openStream(t, server, path)
	var first httpapi.StreamRecord
	if err := json.NewDecoder(response.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if first.Type != "event" || first.Event.Sequence != 1 {
		t.Fatal(first)
	}
	if s.State() != harness.Open {
		t.Fatal("disconnect closed execution")
	}
	if _, err := s.Send(s.Root(), "still running"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	response = openStream(t, server, path+"?after=1")
	defer response.Body.Close()
	d := json.NewDecoder(response.Body)
	previous := uint64(1)
	terminal := false
	for {
		var record httpapi.StreamRecord
		if err := d.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record.Type == "end" {
			break
		}
		if record.Type != "event" || record.Event.Sequence != previous+1 {
			t.Fatal(record, previous)
		}
		previous = record.Event.Sequence
		if record.Event.Kind == "session_closed" {
			terminal = true
		}
	}
	if !terminal {
		t.Fatal("missing terminal record")
	}
}
func TestFinitePagesAgreeWithDirectRead(t *testing.T) {
	_, s := recoverySession(t, true)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	direct, err := s.Events(context.Background(), eventlog.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, s.http, "GET", "/sessions/"+s.ID()+"/events?limit=100", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var adapted eventlog.Page
	if err = json.Unmarshal(w.Body.Bytes(), &adapted); err != nil {
		t.Fatal(err)
	}
	if len(direct.Events) != len(adapted.Events) || direct.Next != adapted.Next || !adapted.Sealed {
		t.Fatal(direct, adapted)
	}
	for i := range direct.Events {
		if direct.Events[i].Sequence != adapted.Events[i].Sequence || string(direct.Events[i].Payload) != string(adapted.Events[i].Payload) {
			t.Fatal(direct.Events[i], adapted.Events[i])
		}
	}
}
