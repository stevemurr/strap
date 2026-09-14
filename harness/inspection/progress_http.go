package inspection

import (
	"github.com/stevemurr/strap/work"
	"net/url"
	"strconv"
)

func ProgressQueryFromValues(v url.Values, brief bool) (ProgressQuery, error) {
	q := ProgressQuery{Mode: v.Get("mode"), WorkID: work.ID(v.Get("work_id")), ReportID: work.ProgressReportID(v.Get("report_id")), FindingID: work.ProgressFindingID(v.Get("finding_id")), BriefID: work.ResearchBriefID(v.Get("brief_id")), EvidenceRef: v.Get("evidence_ref"), Cursor: v.Get("cursor")}
	for k, values := range v {
		if len(values) != 1 || values[0] == "" {
			return q, work.ErrInvalid
		}
		switch k {
		case "actor", "mode", "work_id", "report_id", "finding_id", "brief_id", "evidence_ref", "cursor", "limit", "max_bytes":
		default:
			return q, work.ErrInvalid
		}
	}
	if brief {
		if q.Mode != "" {
			return q, work.ErrInvalid
		}
		q.Mode = "brief"
		if q.Cursor != "" {
			q.Mode = "continue"
		}
	}
	if v.Has("limit") {
		n, e := strconv.Atoi(v.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return q, work.ErrInvalid
		}
		q.Limit = n
	}
	if v.Has("max_bytes") {
		n, e := strconv.Atoi(v.Get("max_bytes"))
		if e != nil || n < 2048 || n > ProgressMaxBytes {
			return q, work.ErrInvalid
		}
		q.MaxBytes = n
	}
	return q, nil
}
