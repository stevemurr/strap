package record

import "github.com/stevemurr/strap/research"

// ResearchControl retains enough metadata to project framed source/report records
// without materializing their bodies. Full text stays in the accepted content.
func ResearchControl(e research.Event) research.Event {
	if e.Source != nil {
		s := *e.Source
		s.Text = ""
		e.Source = &s
	}
	if e.Report != nil {
		p := e.Report
		e.Report = &research.Report{ID: p.ID, Version: p.Version, Binding: p.Binding, StartedAt: p.StartedAt, FinishedAt: p.FinishedAt, Status: p.Status, Spend: p.Spend}
	}
	return e
}
