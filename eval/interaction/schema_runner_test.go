package interaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

func TestSchemaAuditRegressionRunsThroughHarness(t *testing.T) {
	report, err := Run(context.Background(), Options{
		Mode: Scripted, Output: t.TempDir(), Timeout: 10 * time.Second,
		ScenarioIDs: []string{"schema-audit-repair-field"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 {
		t.Fatal(report)
	}
	r := report.Results[0]
	if r.Outcome != "passed" || r.Behavior.CleanSuccess || !r.Behavior.RecoverySuccess || r.Behavior.RejectedCalls != 1 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("invalid schema call must be rejected without effects, then corrected: %+v", r)
	}
}

func TestSchemaFailuresRemainInReportDenominators(t *testing.T) {
	for _, failure := range []string{"setup", "provider"} {
		t.Run(failure, func(t *testing.T) {
			opts := testOptions(t)
			opts.ScenarioIDs = []string{"schema-audit-kind"}
			opts.Provider = testProviderFunc(func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
				return provider.Response{}, errors.New("model endpoint unavailable")
			})
			if failure == "setup" {
				opts.Config.ReasoningLimit = -1
			}
			r := trialResult(t, opts)
			if r.Outcome != "error" || r.ErrorClass != failure || r.Behavior.Scorable || r.Schema == nil || *r.Schema != (SchemaBehavior{}) {
				t.Fatalf("failed schema trial must remain visible and unscored: %+v", r)
			}
			report, err := os.ReadFile(filepath.Join(opts.Output, "report.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(report), "Schema trials: 1. First decisions scored: 0/1; no operation attempted: 0; excluded: 1.") {
				t.Fatalf("failed trial missing from schema denominator: %s", report)
			}
		})
	}
}

// This injects a provider to verify actor selection and the real production
// catalog. It is a runner integration test, not a measured live-model result.
func TestSchemaRunnerUsesEvaluatedRoleAndStopsAfterItsBatch(t *testing.T) {
	for _, id := range []string{"schema-audit-repair-field", "schema-audit-fail-findings", "schema-audit-pass-findings", "schema-progress-objective"} {
		t.Run(id, func(t *testing.T) {
			opts := testOptions(t)
			opts.ScenarioIDs = []string{id}
			var calls atomic.Int32
			opts.Provider = testProviderFunc(func(ctx context.Context, request provider.Request, observer provider.Observer) (provider.Response, error) {
				manifest, err := readManifest(opts.Output, id)
				if err != nil {
					return provider.Response{}, err
				}
				f := manifest.Fixture
				if f.Schema == nil || request.Agent != f.actor() {
					t.Fatalf("provider received another actor's request: %+v", request)
				}
				actual := effectiveRole(manifest.Config, f.Schema.Role)
				if manifest.PromptHash != hashJSON(actual.Prompt) || manifest.ToolsHash != hashJSON(request.Tools) || manifest.ToolsHash != hashJSON(actual.Tools) {
					t.Fatal("manifest must fingerprint the evaluated role's actual prompt and catalog")
				}
				available := map[string]bool{}
				for _, tool := range request.Tools {
					available[tool.Name] = true
				}
				if !available[f.Schema.Operation] || available["assign_work"] || (f.Schema.Role != roster.Root && available["assign_audit"]) {
					t.Fatal("incorrect production role catalog", available)
				}
				switch calls.Add(1) {
				case 1:
					return responseCall("get_work", map[string]any{"work_id": f.Schema.Target.ID}), nil
				case 2:
					script := f.schemaScript(id)
					if _, err := script.Submit(ctx, request, observer); err != nil {
						return provider.Response{}, err
					}
					return script.Submit(ctx, request, observer)
				default:
					t.Error("provider ran after the successful actor batch")
					return provider.Response{Content: "unexpected extra decision"}, nil
				}
			})
			r := trialResult(t, opts)
			if r.Outcome != "passed" || !r.Behavior.CleanSuccess || calls.Load() != 2 || r.Schema == nil || r.Schema.Attempts != 1 || !r.Schema.FirstToolCorrect || !r.Schema.FirstArgumentsValid || r.Schema.InvalidCalls != 0 {
				t.Fatalf("clean first operation after a read must retain separate first-attempt score: %+v", r)
			}
		})
	}
}
