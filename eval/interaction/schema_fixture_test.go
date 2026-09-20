package interaction

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func TestSchemaFixturesProduceInvalidProbeAndExecutableCorrection(t *testing.T) {
	for _, scenario := range schemaScenarios() {
		t.Run(scenario.ID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg := harness.DefaultConfig()
			cfg.Dir, cfg.Web, cfg.LocalTools = t.TempDir(), nil, false
			cfg.Telemetry.ContextTokens = false
			s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: idleProvider{}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Dispose(context.Background())
			f, err := seedSchema(ctx, s, scenario.ID)
			if err != nil {
				t.Fatal(err)
			}
			if f.Schema == nil || f.Schema.Role != schemaRole(scenario.ID) || f.Schema.Actor == "" || f.Schema.Target.ID == "" || f.schemaStimulus(scenario.ID) == "" {
				t.Fatalf("incomplete schema fixture: %+v", f)
			}
			configuration := s.Configuration()
			catalog := configuration.Root.Tools
			switch f.Schema.Role {
			case roster.Auditor:
				catalog = configuration.Auditor.Tools
				if f.Schema.Actor != f.Auditor || f.Schema.Target.Kind != work.AuditWork || f.Original.State != work.Checking || f.Schema.Target.SubjectSubmissionID != f.Submission.ID {
					t.Fatalf("verdict fixture must hold a real assigned audit: %+v", f)
				}
				purpose, err := s.GetWorkProgressReport(ctx, f.Auditor, f.Schema.Target.LatestPositionReportID)
				if err != nil || purpose.Position == nil || purpose.Position.Objective != f.Schema.Target.Task || purpose.Position.Note == "" || purpose.WorkRevision != f.Schema.Target.Revision {
					t.Fatalf("verdict fixture must have completed the required purpose and verification report: %+v, %v", purpose, err)
				}
			case roster.Implementor:
				catalog = configuration.Implementor.Tools
				if f.Schema.Actor != f.Implementor || f.Original.State != work.Active || f.Submission.ID != "" || f.Original.LatestProgressReportID != "" {
					t.Fatalf("progress fixture must start with an active unreported assignment: %+v", f)
				}
			case roster.Root:
				if f.Schema.Actor != f.Root || f.Original.State != work.NeedsCheck || f.Submission.ID == "" || f.PreviousSubmission == "" {
					t.Fatalf("assignment fixture must retain its replacement submission: %+v", f)
				}
			}
			p := f.schemaScript(scenario.ID)
			bad := nextSchemaFixtureCall(t, ctx, p)
			good := nextSchemaFixtureCall(t, ctx, p)
			if bad.ID == good.ID || good.Name != f.Schema.Operation {
				t.Fatalf("probe and correction must be distinct calls to the target operation: bad=%+v good=%+v", bad, good)
			}
			if schemaFixtureAccepts(t, catalog, bad) || !schemaFixtureAccepts(t, catalog, good) {
				t.Fatalf("fixture did not isolate schema rejection followed by correction: bad=%+v good=%+v", bad, good)
			}
			// These public commands check that the correction is meaningful against
			// actual seeded state, not just syntactically valid. The runnable trials
			// separately send both calls through the actor's production dispatcher.
			switch f.Schema.Role {
			case roster.Root:
				request, err := tool.DecodeAssignment(good.Name, good.Arguments)
				if err != nil {
					t.Fatal(err)
				}
				audit, err := s.AssignWork(ctx, f.Schema.Actor, request)
				if err != nil || audit.ParentID != f.Original.ID || audit.SubjectSubmissionID != f.Submission.ID || audit.Assignee != f.Auditor {
					t.Fatalf("corrected assignment must bind current fixture: %+v, %v", audit, err)
				}
			case roster.Auditor:
				var request work.AuditRequest
				if err := decodeSchemaArgs(good.Arguments, &request); err != nil {
					t.Fatal(err)
				}
				audit, err := s.SubmitAudit(ctx, f.Schema.Actor, request)
				if err != nil || audit.WorkID != f.Schema.Target.ID || audit.SubmissionID != f.Submission.ID || audit.Verdict != f.Schema.Verdict {
					t.Fatalf("corrected verdict must apply to assigned submission: %+v, %v", audit, err)
				}
				original, err := s.GetWork(ctx, f.Root, f.Original.ID)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := s.GetPlan(ctx, f.Root, f.Plan.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantWork, wantStep := work.Accepted, work.Completed
				if f.Schema.Verdict == work.Fail {
					wantWork, wantStep = work.ChangesRequested, work.Pending
					if len(audit.Findings) != 1 || !reflect.DeepEqual(audit.Findings[0].StepIDs, []work.StepID{f.Plan.Steps[0].ID}) {
						t.Fatalf("failure must identify the actual scoped step: %+v", audit)
					}
				}
				if original.State != wantWork || plan.Steps[0].Status != wantStep || original.Revision != f.Original.Revision+1 {
					t.Fatalf("verdict did not produce expected transition: original=%+v plan=%+v", original, plan)
				}
			case roster.Implementor:
				request, err := tool.DecodeProgressReport(good.Arguments)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := s.ReportWorkProgress(ctx, f.Schema.Actor, request)
				if err != nil {
					t.Fatal(err)
				}
				report, err := s.GetWorkProgressReport(ctx, f.Root, receipt.ReportID)
				if err != nil || report.Position == nil || !reflect.DeepEqual(*report.Position, f.Schema.ExpectedPosition) || report.WorkRevision != f.Original.Revision+1 || report.AssignedAtRevision != f.Original.AssignedAtRevision {
					t.Fatalf("corrected progress must preserve assignment and objective: %+v, %v", report, err)
				}
				plan, err := s.GetPlan(ctx, f.Root, f.Plan.ID)
				if err != nil || !reflect.DeepEqual(plan, f.Plan) {
					t.Fatalf("progress fixture must leave the plan unchanged: %+v, %v", plan, err)
				}
			}
		})
	}
}

func nextSchemaFixtureCall(t *testing.T, ctx context.Context, p provider.Provider) provider.ToolCall {
	t.Helper()
	r, err := p.Submit(ctx, provider.Request{}, nil)
	if err != nil || len(r.ToolCalls) != 1 {
		t.Fatalf("expected one fixture tool call: %+v, %v", r, err)
	}
	return r.ToolCalls[0]
}

func schemaFixtureAccepts(t *testing.T, tools []provider.ToolDefinition, call provider.ToolCall) bool {
	t.Helper()
	for _, definition := range tools {
		if definition.Name != call.Name {
			continue
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(definition.Parameters))
		if err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		const location = "https://strap.test/schema-fixture.json"
		if err := compiler.AddResource(location, doc); err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile(location)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(call.Arguments))
		if err != nil {
			t.Fatal(err)
		}
		return schema.Validate(value) == nil
	}
	return false
}
