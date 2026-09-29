package harness

import "testing"

// Reads reach a model without event-log cursors, wherever they are nested,
// and with every other field in its original order.
func TestModelJSONDropsLogCursorsOnly(t *testing.T) {
	type cursor struct {
		Session  string `json:"session"`
		Sequence uint64 `json:"sequence"`
	}
	type item struct {
		WorkID string `json:"work_id"`
		Record cursor `json:"record"`
	}
	v := struct {
		Zeta    string `json:"zeta"`
		Through cursor `json:"through"`
		Items   []item `json:"items"`
		Other   struct {
			Through int `json:"through"`
		} `json:"other"`
		Alpha string `json:"alpha"`
	}{Zeta: "z <tag>", Through: cursor{"s", 706}, Items: []item{{"work-1", cursor{"s", 3}}}, Alpha: "a"}
	v.Other.Through = 7
	got, err := modelJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	// HTML escaping is unchanged: tool.JSON always escaped it.
	want := "{\"zeta\":\"z \\u003ctag\\u003e\",\"items\":[{\"work_id\":\"work-1\"}],\"other\":{\"through\":7},\"alpha\":\"a\"}"
	if text := got.Content.Text(); text != want {
		t.Fatalf("got  %q\nwant %q", text, want)
	}
}

// Agent reads also drop state_revision, wherever it is nested, which ordinary
// reads keep: its value depends on how an agent's notices were batched.
func TestAgentJSONDropsStateRevision(t *testing.T) {
	type info struct {
		StateRevision uint64 `json:"state_revision"`
		AgentID       string `json:"agent_id"`
		State         string `json:"state"`
	}
	v := struct {
		Agents []info `json:"agents"`
		Agent  info   `json:"agent"`
	}{Agents: []info{{6, "agent-2", "running"}}, Agent: info{4, "agent-3", "idle"}}
	got, err := agentJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"agents":[{"agent_id":"agent-2","state":"running"}],"agent":{"agent_id":"agent-3","state":"idle"}}`
	if text := got.Content.Text(); text != want {
		t.Fatalf("got  %q\nwant %q", text, want)
	}
	kept, err := modelJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if text := kept.Content.Text(); text == want {
		t.Fatal("modelJSON dropped state_revision too")
	}
}
