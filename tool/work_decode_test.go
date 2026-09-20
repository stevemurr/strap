package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/work"
)

// HTTP uses these pure decoders. Compare their domain values to actual tool
// handlers, including null/empty distinctions and nested optional values.
func checkSharedDecoder[A any](t *testing.T, build func(Handler[A]) Tool, decode func(json.RawMessage) (A, error), inputs []string) {
	t.Helper()
	var got A
	calls := 0
	op := build(func(_ context.Context, _ Call, a A) (Result, error) { got = a; calls++; return Text("ok"), nil })
	for _, input := range inputs {
		raw := json.RawMessage(input)
		want, err := decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		before := calls
		if _, err = op.Call(context.Background(), Call{Arguments: raw}); err != nil || calls != before+1 || !reflect.DeepEqual(got, want) {
			t.Fatalf("decoder/tool mismatch: got=%#v want=%#v err=%v", got, want, err)
		}
		var envelope map[string]map[string]json.RawMessage
		if err = json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		// Every omission must be rejected by both paths, without invoking a handler.
		for field := range envelope["input"] {
			value := envelope["input"][field]
			delete(envelope["input"], field)
			bad, _ := json.Marshal(envelope)
			before = calls
			_, de := decode(bad)
			_, ce := op.Call(context.Background(), Call{Arguments: bad})
			if de == nil || ce == nil || calls != before {
				t.Fatalf("accepted omission %s: decoder=%v call=%v", field, de, ce)
			}
			envelope["input"][field] = value
		}
		for _, bad := range []json.RawMessage{[]byte(`{}`), []byte(`{"input":null}`), []byte(`{"input":{},"extra":true}`)} {
			before = calls
			_, de := decode(bad)
			_, ce := op.Call(context.Background(), Call{Arguments: bad})
			if de == nil || ce == nil || calls != before {
				t.Fatal("accepted malformed envelope", string(bad))
			}
		}
	}
}

func TestSharedWorkDecodersMatchToolDomainConversions(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		checkSharedDecoder[work.CancelRequest](t, CancelWork, DecodeCancellation, []string{`{"input":{"work_id":"w","expected_revision":1,"reason":"withdrawn"}}`})
	})
	t.Run("submit", func(t *testing.T) {
		checkSharedDecoder[work.SubmitRequest](t, SubmitWork, DecodeSubmission, []string{
			`{"input":{"work_id":"w","expected_revision":1,"summary":"done","evidence":null,"artifacts":null}}`,
			`{"input":{"work_id":"w","expected_revision":1,"summary":"done","evidence":[],"artifacts":[]}}`,
			`{"input":{"work_id":"w","expected_revision":1,"summary":"done","evidence":["checked"],"artifacts":[{"uri":"file:x","revision":null},{"uri":"file:y","revision":"r"}]}}`,
		})
	})
	t.Run("audit", func(t *testing.T) {
		checkSharedDecoder[work.AuditRequest](t, SubmitAudit, DecodeAuditSubmission, []string{
			`{"input":{"work_id":"w","expected_revision":1,"submission_id":"s","summary":"checked","verdict":"pass","findings":null}}`,
			`{"input":{"work_id":"w","expected_revision":1,"submission_id":"s","summary":"checked","verdict":"pass","findings":[]}}`,
			`{"input":{"work_id":"w","expected_revision":1,"submission_id":"s","summary":"broken","verdict":"fail","findings":[{"description":"d","required_change":"c","verification":"v","step_ids":null}]}}`,
		})
	})
	t.Run("research", func(t *testing.T) {
		checkSharedDecoder[work.SubmitResearchRequest](t, SubmitResearch, DecodeResearchSubmission, []string{
			`{"input":{"work_id":"w","expected_revision":1,"summary":"done","finding_ids":null,"open_questions":null,"recommendation":null,"proposed_steps":null}}`,
			`{"input":{"work_id":"w","expected_revision":1,"summary":"done","finding_ids":[],"open_questions":[],"recommendation":"","proposed_steps":[]}}`,
			`{"input":{"work_id":"w","expected_revision":1,"summary":"done","finding_ids":["f"],"open_questions":["q"],"recommendation":"r","proposed_steps":[{"title":"verify","acceptance_criteria":null}]}}`,
		})
	})
}
