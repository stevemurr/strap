package httpapi_test

import (
	"encoding/json"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
	"testing"
)

func TestResearchProgressHTTPRoundTrip(t *testing.T) {
	ctx, s := recoverySession(t, true)
	reg, err := s.CreateAgent(ctx, s.Root(), roster.CreateRequest{Role: roster.Researcher})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: reg.AgentID, Task: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/sessions/" + s.ID()
	r := work.ReportWorkProgressRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Position: &work.WorkPosition{Objective: "Compare requirements"}, Findings: []work.ProgressFindingDraft{{Claim: "unverified concern", Basis: work.Inferred, Limitation: "not executed"}}}
	response := request(t, s.http, "POST", base+"/work/report-progress", httpapi.WorkRequest[work.ReportWorkProgressRequest]{Actor: reg.AgentID, Request: r})
	var receipt work.ReportWorkProgressResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &receipt) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, path := range []string{"progress/" + string(w.ID), "progress-reports/" + string(receipt.ReportID), "progress-findings/" + string(receipt.FindingIDs[0])} {
		got := request(t, s.http, "GET", base+"/"+path+"?actor="+string(s.Root()), nil)
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		denied := request(t, s.http, "GET", base+"/"+path+"?actor=unknown", nil)
		if denied.Code != 403 {
			t.Fatal(denied.Code)
		}
	}
	stale := request(t, s.http, "POST", base+"/work/report-progress", httpapi.WorkRequest[work.ReportWorkProgressRequest]{Actor: reg.AgentID, Request: r})
	if stale.Code != 409 {
		t.Fatal(stale.Code)
	}
	brief := work.SubmitResearchRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: receipt.WorkRevision}, Summary: "Need a runtime check", FindingIDs: receipt.FindingIDs}
	delivered := request(t, s.http, "POST", base+"/work/research", httpapi.WorkRequest[work.SubmitResearchRequest]{Actor: reg.AgentID, Request: brief})
	var b work.SubmitResearchResult
	if delivered.Code != 200 || json.Unmarshal(delivered.Body.Bytes(), &b) != nil {
		t.Fatal(delivered.Code, delivered.Body.String())
	}
	got := request(t, s.http, "GET", base+"/research-briefs/"+string(b.BriefID)+"?actor="+string(s.Root()), nil)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
}
