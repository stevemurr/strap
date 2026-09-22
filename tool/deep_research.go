package tool

import (
	"context"

	"github.com/stevemurr/strap/research"
)

func DeepResearch(h Handler[research.Request]) Tool {
	return builtin("deep_research", "Investigate a bounded question for your active research assignment. Runs for minutes with adaptive web research and a separate citation verification pass. Supply explicit success criteria before starting. The result is a bounded digest; get_research_report reads retained findings and source text. Cancellation preserves partial evidence. This does not submit research or mark work delivered. All fields are required; use null for defaults. Domain filters select eligible sources, not a network sandbox. A max_tokens cap requires a provider with token counting and a configured output limit.", h,
		MinLength("work_id", 1), MinLength("question", 1), MinItems("success_criteria", 1), MaxItems("success_criteria", 8), MinLength("success_criteria[]", 1),
		Nullable("context", "no additional context"), Nullable("must_cover", "no additional topics"), MaxItems("must_cover", 12), MinLength("must_cover[]", 1),
		Nullable("allow_domains", "any eligible domain"), Nullable("block_domains", "no excluded domains"), MaxItems("allow_domains", 32), MaxItems("block_domains", 32),
		Nullable("depth", "standard"), Enum("depth", "survey", "standard", "exhaustive"), Nullable("max_minutes", "use the preset deadline"), Minimum("max_minutes", 1), Maximum("max_minutes", 20),
		Nullable("max_tokens", "use host token policy"), Minimum("max_tokens", 1), Maximum("max_tokens", 1_000_000_000))
}

type deepResearchReadInput struct {
	Mode     string  `json:"mode"`
	WorkID   *string `json:"work_id"`
	ReportID *string `json:"report_id"`
	SourceID *string `json:"source_id"`
	Cursor   *string `json:"cursor"`
	MaxBytes *int    `json:"max_bytes"`
}

func GetDeepResearchReport(h Handler[research.ReadQuery]) Tool {
	return builtin("get_research_report", "Read retained deep research without waking workers. Use runs with work_id, report or sources with report_id, source with report_id and source_id, or continue with cursor. Supply null for unused selectors. Results pin a log prefix; JSON fragments must be reassembled before interpretation. Report-local claim IDs are not work-ledger finding IDs.", func(ctx context.Context, c Call, a deepResearchReadInput) (Result, error) {
		return h(ctx, c, research.ReadQuery{Mode: a.Mode, WorkID: valueOrZero(a.WorkID), ReportID: valueOrZero(a.ReportID), SourceID: valueOrZero(a.SourceID), Cursor: valueOrZero(a.Cursor), MaxBytes: valueOrZero(a.MaxBytes)})
	}, Enum("mode", "runs", "report", "sources", "source", "continue"), Nullable("work_id", "not selected"), Nullable("report_id", "not selected"), Nullable("source_id", "not selected"), Nullable("cursor", "first page"), Nullable("max_bytes", "default 16 KiB"), Minimum("max_bytes", 2048), Maximum("max_bytes", 32768))
}
