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
		`{"input":{"position":{"objective":"inspect","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`,
		`{"input":{"work_id":"w","note":"legacy"}}`,
		// The assignment binding is recorded from current work, never sent.
		`{"input":{"work_id":"w","assigned_at_revision":1,"position":{"objective":"inspect","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`,
		`{"input":{"work_id":"w"}}`,
		`{"input":{"work_id":"w","position":null,"findings":null,"steps":null}}`,
		`{"input":{"work_id":"w","steps":[{"step_id":"s","status":"completed","note":null}],"position":null,"findings":null}}`,
	} {
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request reached handler")
	}
	if _, err := op.Call(context.Background(), Call{Arguments: []byte(`{"input":{"work_id":"w","position":{"objective":"inspect","blocker":"","activity":null,"note":null,"next_step":null,"uncertainty":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`)}); err != nil {
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
	seed := `{"input":{"work_id":"w","position":{"objective":"inspect","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"position", seed, true},
		{"findings", `{"input":{"work_id":"w","findings":[{"claim":"passes","basis":"observed","evidence":[{"uri":"execution:x","revision":null,"locator":null,"detail":null}],"limitation":null,"supersedes":null}],"position":null,"steps":null}}`, true},
		{"step", `{"input":{"work_id":"w","steps":[{"step_id":"s","status":"ready_for_review","note":null}],"position":null,"findings":null}}`, true},
		{"no intent", `{"input":{"work_id":"w"}}`, false},
		{"top level alias", `{"input":{"work_id":"w","objective":"inspect","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null}}`, false},
		{"nested extra", `{"input":{"work_id":"w","position":{"objective":"inspect","owner":"forged","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`, false},
		{"null optional", `{"input":{"work_id":"w","position":{"objective":"inspect","blocker":null,"activity":null,"note":null,"next_step":null,"uncertainty":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`, true},
		{"missing evidence uri", `{"input":{"work_id":"w","findings":[{"claim":"passes","basis":"observed","evidence":[{}],"limitation":null,"supersedes":null}],"position":null,"steps":null}}`, false},
		{"unknown finding basis", `{"input":{"work_id":"w","findings":[{"claim":"passes","basis":"guessed","evidence":null,"limitation":null,"supersedes":null}],"position":null,"steps":null}}`, false},
		{"invalid step transition", `{"input":{"work_id":"w","steps":[{"step_id":"s","status":"completed","note":null}],"position":null,"findings":null}}`, false},
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
		`{"input":{"work_id":"w","work_id":"forged","position":{"objective":"inspect","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`,
		`{"input":{"work_id":"w","position":{"objective":"inspect","objective":"overwritten","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null},"findings":null,"steps":null}}`,
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

// A revision is advertised only up to the largest integer JSON carries exactly,
// and every notation for that value decodes to it without passing through a
// float. submit_work still carries one, so the guarantee is checked there now
// that progress reports name only their work.
func TestSubmitWorkPreservesExactRevisions(t *testing.T) {
	for _, tc := range []struct {
		token    string
		revision work.Revision
	}{
		{"9007199254740991", 9007199254740991},
		{"90071992547409910e-1", 9007199254740991},
		{"9007199254740991.0", 9007199254740991},
	} {
		t.Run(tc.token, func(t *testing.T) {
			var got work.SubmitRequest
			op := SubmitWork(func(_ context.Context, _ Call, request work.SubmitRequest) (Result, error) {
				got = request
				return Text("submitted"), nil
			})
			raw := json.RawMessage(`{"input":{"work_id":"w","expected_revision":` + tc.token + `,"summary":"done","evidence":null,"artifacts":null}}`)
			if err := validateExportedSchema(t, compileExportedSchema(t, op.Definition().Parameters), raw); err != nil {
				t.Fatal(err)
			}
			if _, err := op.Call(context.Background(), Call{Arguments: raw}); err != nil {
				t.Fatal(err)
			}
			if got.ExpectedRevision != tc.revision {
				t.Fatalf("revision rounded: handler=%+v want=%d", got, tc.revision)
			}
		})
	}
}

// Recorded rejections cluster on a few shapes: the position or a whole finding
// flattened onto the request, and evidence abbreviated or collapsed into the
// finding. Each has to name the field the author meant, because "not an allowed
// field" alone leaves the author guessing where it should have gone.
func TestProgressNamesTheFieldAMisplacedValueBelongsTo(t *testing.T) {
	op := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		return Text("recorded"), nil
	})
	for _, c := range []struct{ raw, want string }{
		{`{"input":{"work_id":"w","objective":"inspect","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null}}`, "objective belongs to position"},
		{`{"input":{"work_id":"w","claim":"it builds","basis":"observed"}}`, "a finding belongs in findings"},
		{`{"input":{"work_id":"w","findings":[{"claim":"c","basis":"observed","e":[{"uri":"file:x"}],"evidence":null,"limitation":null,"supersedes":null}],"position":null,"steps":null}}`, "the field is evidence"},
		{`{"input":{"work_id":"w","findings":[{"claim":"c","basis":"observed","uri":"file:x","evidence":null,"limitation":null,"supersedes":null}],"position":null,"steps":null}}`, "evidence is an array of objects on the finding"},
		{`{"input":{"work_id":"w","findings":[{"claim":"c","basis":"inferred","limit":"only one case","evidence":null,"limitation":null,"supersedes":null}],"position":null,"steps":null}}`, "the field is limitation"},
	} {
		_, err := op.Call(context.Background(), Call{Arguments: []byte(c.raw)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s\n  got  %v\n  want a message containing %q", c.raw, err, c.want)
		}
	}
}
