package tool

import (
	"context"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type ProgressReadArgs struct {
	Mode        string
	WorkID      work.ID
	ReportID    work.ProgressReportID
	FindingID   work.ProgressFindingID
	BriefID     work.ResearchBriefID
	EvidenceRef string
	Cursor      string
	Limit       int
	MaxBytes    int
}

func GetWorkProgress(h Handler[ProgressReadArgs]) Tool {
	type current struct {
		Mode     string  `json:"mode"`
		WorkID   work.ID `json:"work_id"`
		MaxBytes *int    `json:"max_bytes"`
	}
	type collection struct {
		Mode     string  `json:"mode"`
		WorkID   work.ID `json:"work_id"`
		Limit    *int    `json:"limit"`
		MaxBytes *int    `json:"max_bytes"`
	}
	type report struct {
		Mode     string                `json:"mode"`
		ID       work.ProgressReportID `json:"report_id"`
		MaxBytes *int                  `json:"max_bytes"`
	}
	type finding struct {
		Mode     string                 `json:"mode"`
		ID       work.ProgressFindingID `json:"finding_id"`
		MaxBytes *int                   `json:"max_bytes"`
	}
	type evidence struct {
		Mode     string `json:"mode"`
		Ref      string `json:"evidence_ref"`
		MaxBytes *int   `json:"max_bytes"`
	}
	type next struct {
		Mode   string `json:"mode"`
		Cursor string `json:"cursor"`
	}
	budget := []Constraint{Nullable("max_bytes", "use the default page budget"), Minimum("max_bytes", 2048), Maximum("max_bytes", 32768)}
	return compose(provider.ToolDefinition{Name: "get_work_progress", Description: "Read recorded work progress without waking workers. Select current, report, reports, finding, findings, or evidence. Follow next_cursor with only mode continue and cursor. Pages pin a snapshot; oversized records return fragments of their serialized JSON and must be reassembled before interpretation. Report and assignment revisions identify freshness. Complete evidence reads include only output retained by execution capture."},
		builtin("current", "", func(ctx context.Context, c Call, a current) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: a.Mode, WorkID: a.WorkID, MaxBytes: valueOrZero(a.MaxBytes)})
		}, append(budget, Enum("mode", "current"), MinLength("work_id", 1))...),
		builtin("collection", "", func(ctx context.Context, c Call, a collection) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: a.Mode, WorkID: a.WorkID, Limit: valueOrZero(a.Limit), MaxBytes: valueOrZero(a.MaxBytes)})
		}, append(budget, Enum("mode", "reports", "findings"), Nullable("limit", "use the default page size"), MinLength("work_id", 1), Minimum("limit", 1), Maximum("limit", 100))...),
		builtin("report", "", func(ctx context.Context, c Call, a report) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: a.Mode, ReportID: a.ID, MaxBytes: valueOrZero(a.MaxBytes)})
		}, append(budget, Enum("mode", "report"), MinLength("report_id", 1))...),
		builtin("finding", "", func(ctx context.Context, c Call, a finding) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: a.Mode, FindingID: a.ID, MaxBytes: valueOrZero(a.MaxBytes)})
		}, append(budget, Enum("mode", "finding"), MinLength("finding_id", 1))...),
		builtin("evidence", "", func(ctx context.Context, c Call, a evidence) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: a.Mode, EvidenceRef: a.Ref, MaxBytes: valueOrZero(a.MaxBytes)})
		}, append(budget, Enum("mode", "evidence"), MinLength("evidence_ref", 1))...),
		builtin("continue", "", func(ctx context.Context, c Call, a next) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: a.Mode, Cursor: a.Cursor})
		}, Enum("mode", "continue"), MinLength("cursor", 1)),
	)
}
func GetResearchBrief(h Handler[ProgressReadArgs]) Tool {
	type first struct {
		ID       work.ResearchBriefID `json:"brief_id"`
		MaxBytes *int                 `json:"max_bytes"`
	}
	type next struct {
		Cursor string `json:"cursor"`
	}
	return compose(provider.ToolDefinition{Name: "get_research_brief", Description: "Read an immutable delivered research brief by brief_id. Continue with cursor alone. Finding IDs resolve through get_work_progress. Oversized records return bounded JSON fragments; delivery does not mean independent verification."},
		builtin("brief", "", func(ctx context.Context, c Call, a first) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: "brief", BriefID: a.ID, MaxBytes: valueOrZero(a.MaxBytes)})
		}, Nullable("max_bytes", "use the default page budget"), MinLength("brief_id", 1), Minimum("max_bytes", 2048), Maximum("max_bytes", 32768)),
		builtin("continue", "", func(ctx context.Context, c Call, a next) (Result, error) {
			return h(ctx, c, ProgressReadArgs{Mode: "continue", Cursor: a.Cursor})
		}, MinLength("cursor", 1)))
}
