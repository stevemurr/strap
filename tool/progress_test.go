package tool

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/work"
	"strings"
	"testing"
)

func TestProgressReportRequiresTargetAndIntent(t *testing.T) {
	calls := 0
	op := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("recorded"), nil
	})
	for _, raw := range []string{
		`{"work_id":"w","position":{"objective":"inspect"}}`,
		`{"expected_revision":1,"position":{"objective":"inspect"}}`,
		`{"work_id":"w","expected_revision":1,"note":"legacy"}`,
		// The assignment binding is recorded from current work, never sent.
		`{"work_id":"w","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"inspect"}}`,
		`{"work_id":"w","expected_revision":1}`,
		`{"work_id":"w","expected_revision":1,"position":null}`,
		`{"work_id":"w","expected_revision":1,"steps":[{"step_id":"s","status":"completed"}]}`,
	} {
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request reached handler")
	}
	if _, err := op.Call(context.Background(), Call{Arguments: []byte(`{"work_id":"w","expected_revision":1,"position":{"objective":"inspect","blocker":""}}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestProgressSchemaAndDecoderConform(t *testing.T) {
	calls := 0
	op := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("recorded"), nil
	})
	schema := compileExportedSchema(t, op.Definition().Parameters)
	seed := `{"work_id":"w","expected_revision":2,"position":{"objective":"inspect"}}`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"position", seed, true},
		{"findings", `{"work_id":"w","expected_revision":2,"findings":[{"claim":"passes","basis":"observed","evidence":[{"uri":"execution:x"}]}]}`, true},
		{"step", `{"work_id":"w","expected_revision":2,"steps":[{"step_id":"s","status":"ready_for_review"}]}`, true},
		{"no intent", `{"work_id":"w","expected_revision":2}`, false},
		{"top level alias", `{"work_id":"w","expected_revision":2,"objective":"inspect"}`, false},
		{"nested extra", `{"work_id":"w","expected_revision":2,"position":{"objective":"inspect","owner":"forged"}}`, false},
		{"null optional", `{"work_id":"w","expected_revision":2,"position":{"objective":"inspect","blocker":null}}`, false},
		{"missing evidence uri", `{"work_id":"w","expected_revision":2,"findings":[{"claim":"passes","basis":"observed","evidence":[{}]}]}`, false},
		{"unknown finding basis", `{"work_id":"w","expected_revision":2,"findings":[{"claim":"passes","basis":"guessed"}]}`, false},
		{"invalid step transition", `{"work_id":"w","expected_revision":2,"steps":[{"step_id":"s","status":"completed"}]}`, false},
		{"too many findings", string(mutateSchemaSeed(t, seed, schemaMutation{path: "findings", replacement: `[` + strings.Repeat(`{"claim":"c","basis":"observed"},`, 16) + `{"claim":"c","basis":"observed"}]`})), false},
		{"too many evidence refs", string(mutateSchemaSeed(t, seed, schemaMutation{path: "findings", replacement: `[{"claim":"c","basis":"observed","evidence":[` + strings.Repeat(`{"uri":"execution:x"},`, 8) + `{"uri":"execution:x"}]}]`})), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			raw := json.RawMessage(tc.raw)
			schemaErr := validateExportedSchema(t, schema, raw)
			_, decodeErr := DecodeProgressReport(raw)
			_, callErr := op.Call(context.Background(), Call{Arguments: raw})
			if (schemaErr == nil) != tc.valid || (decodeErr == nil) != tc.valid || (callErr == nil) != tc.valid {
				t.Fatalf("want accepted=%v schema=%v decoder=%v call=%v", tc.valid, schemaErr, decodeErr, callErr)
			}
			if got := calls - before; tc.valid && got != 1 || !tc.valid && got != 0 {
				t.Fatalf("handler called %d times for valid=%v", got, tc.valid)
			}
		})
	}
}

// Duplicate keys are a wire-format error, before JSON Schema validation. Calling
// the actual tool guards against a wrapper erasing duplicates while normalizing.
func TestProgressRejectsDuplicateKeysBeforeHandler(t *testing.T) {
	calls := 0
	op := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("recorded"), nil
	})
	for _, raw := range []string{
		`{"work_id":"w","work_id":"forged","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"inspect"}}`,
		`{"work_id":"w","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"inspect","objective":"overwritten"}}`,
	} {
		if _, err := DecodeProgressReport([]byte(raw)); err == nil {
			t.Fatalf("decoder accepted duplicate keys: %s", raw)
		}
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatalf("tool accepted duplicate keys: %s", raw)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input reached handler %d times", calls)
	}
}

func TestProgressPreservesExactRevisions(t *testing.T) {
	for _, tc := range []struct {
		token    string
		revision work.Revision
	}{
		{"9007199254740993", 9007199254740993},
		{"18446744073709551615", 18446744073709551615},
		{"184467440737095516150e-1", 18446744073709551615},
	} {
		t.Run(tc.token, func(t *testing.T) {
			var got work.ReportWorkProgressRequest
			op := ReportWorkProgress(func(_ context.Context, _ Call, request work.ReportWorkProgressRequest) (Result, error) {
				got = request
				return Text("recorded"), nil
			})
			raw := json.RawMessage(`{"work_id":"w","expected_revision":` + tc.token + `,"position":{"objective":"inspect"}}`)
			if err := validateExportedSchema(t, compileExportedSchema(t, op.Definition().Parameters), raw); err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeProgressReport(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := op.Call(context.Background(), Call{Arguments: raw}); err != nil {
				t.Fatal(err)
			}
			if got.ExpectedRevision != tc.revision || decoded.ExpectedRevision != tc.revision {
				t.Fatalf("revision rounded: handler=%+v decoder=%+v want=%d", got, decoded, tc.revision)
			}
		})
	}
}
