package httpapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func TestResearchProgressHTTPRoundTrip(t *testing.T) {
	ctx, s := recoverySession(t, true)
	reg, err := s.CreateAgent(ctx, s.Manager(), roster.CreateRequest{Role: roster.WebResearcher})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.WebResearch, Assignee: reg.AgentID, Task: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/sessions/" + s.ID()
	limitation := "not executed"
	r := tool.ReportWorkProgressInput{WorkID: w.ID, Position: &tool.WorkPositionInput{Objective: "Compare requirements"}, Findings: []tool.ProgressFindingDraftInput{{Claim: "unverified concern", Basis: work.Inferred, Limitation: &limitation}}}
	response := request(t, s.http, "POST", base+"/work/report-progress", wireRequest(reg.AgentID, r))
	var receipt work.ReportWorkProgressResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &receipt) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, path := range []string{"progress/" + string(w.ID), "progress-reports/" + string(receipt.ReportID), "progress-findings/" + string(receipt.FindingIDs[0])} {
		got := request(t, s.http, "GET", base+"/"+path+"?actor="+string(s.Manager()), nil)
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		denied := request(t, s.http, "GET", base+"/"+path+"?actor=unknown", nil)
		if denied.Code != 403 {
			t.Fatal(denied.Code)
		}
	}
	// A report names only its work, so repeating one records a second report
	// rather than colliding with a revision the caller no longer carries.
	repeat := request(t, s.http, "POST", base+"/work/report-progress", wireRequest(reg.AgentID, r))
	var second work.ReportWorkProgressResult
	if repeat.Code != 200 || json.Unmarshal(repeat.Body.Bytes(), &second) != nil || second.ReportID == receipt.ReportID {
		t.Fatal(repeat.Code, repeat.Body.String())
	}
	brief := tool.SubmitBriefInput{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: second.WorkRevision}, Summary: "Need a runtime check", FindingIDs: receipt.FindingIDs}
	delivered := request(t, s.http, "POST", base+"/work/brief", wireRequest(reg.AgentID, brief))
	var b work.SubmitBriefResult
	if delivered.Code != 200 || json.Unmarshal(delivered.Body.Bytes(), &b) != nil {
		t.Fatal(delivered.Code, delivered.Body.String())
	}
	got := request(t, s.http, "GET", base+"/research-briefs/"+string(b.BriefID)+"?actor="+string(s.Manager()), nil)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
}
