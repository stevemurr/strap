package harness

import (
	"bytes"
	"encoding/json"

	"github.com/stevemurr/strap/tool"
)

// modelJSON renders a read for a model without the event-log positions it
// carries: a read's "through" cursor, which tells hosts which prefix of the log
// it reflects, and the "record" cursor of cited execution evidence among them.
// A model continues with the signed cursor tokens and cites evidence by its
// evidence_ref, so it has no use for a raw log position, which also depends on
// how many streaming and telemetry records happened to be written, so no
// replay could reproduce it. Any field whose value is a log cursor is dropped;
// every other field keeps its order.
func modelJSON(v any) (tool.Result, error) {
	return filteredJSON(v, func(string, json.RawMessage) bool { return false })
}

// agentJSON renders an agent read for a model, as modelJSON does, and also
// without state_revision. It counts the agent's running and idle transitions,
// which depend on how quickly its inbox notices were taken rather than on its
// work; no tool takes it, and a replay taking notices in other batches sees
// another number (ladder easy-19, 2026-09-25).
func agentJSON(v any) (tool.Result, error) {
	return filteredJSON(v, func(key string, _ json.RawMessage) bool { return key == "state_revision" })
}

// filteredJSON drops every log cursor and every field drop names, keeping the
// order of the rest.
func filteredJSON(v any, drop func(key string, value json.RawMessage) bool) (tool.Result, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return tool.Result{}, err
	}
	var out bytes.Buffer
	if err := withoutFields(&out, raw, drop); err != nil {
		return tool.Result{}, err
	}
	return tool.JSON(json.RawMessage(out.Bytes()))
}

func withoutFields(out *bytes.Buffer, raw json.RawMessage, drop func(string, json.RawMessage) bool) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '{':
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.Token() // {
		out.WriteByte('{')
		first := true
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return err
			}
			if name, _ := key.(string); isLogCursor(value) || drop(name, value) {
				continue
			}
			if !first {
				out.WriteByte(',')
			}
			first = false
			name, _ := json.Marshal(key)
			out.Write(name)
			out.WriteByte(':')
			if err := withoutFields(out, value, drop); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		out.WriteByte('[')
		for i, item := range items {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := withoutFields(out, item, drop); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	default:
		out.Write(raw)
	}
	return nil
}

// isLogCursor reports whether a value is an event-log cursor.
func isLogCursor(raw json.RawMessage) bool {
	var c map[string]json.RawMessage
	if json.Unmarshal(raw, &c) != nil {
		return false
	}
	_, session := c["session"]
	_, sequence := c["sequence"]
	return session && sequence
}
