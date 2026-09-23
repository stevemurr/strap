package deepresearch

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stevemurr/strap/research"
)

func TestScoringSeparatesCitationValidityFromSupport(t *testing.T) {
	source := research.Source{ID: "source-1", FinalURL: "https://lab.test/measurement", Text: "Measured: 12 ms."}
	hash := sha256.Sum256([]byte(source.Text))
	source.SHA256, source.Bytes = hex.EncodeToString(hash[:]), len(source.Text)
	ref := research.Citation{SourceID: source.ID, Start: 0, End: len(source.Text), Quote: source.Text, URI: source.FinalURL, Revision: "sha256:" + source.SHA256, Locator: "report/source-1#bytes=0-16"}
	if len(source.Text) != 16 {
		t.Fatal("unexpected fixture length")
	}
	p := research.Report{ID: "report", Claims: []research.Claim{{ID: "claim-1", Claim: "Measured: 99 ms.", Verdict: "supported", Evidence: []research.Citation{ref}}}}
	ungraded := Evaluate(p, []research.Source{source}, 1, Labels{})
	if ungraded.Faithfulness != nil || ungraded.Groundedness != nil || ungraded.Coverage != nil || ungraded.CitationValidity.Numerator != 1 {
		t.Fatal(ungraded)
	}
	graded := Evaluate(p, []research.Source{source}, 1, Labels{Claims: map[string]bool{"claim-1": false}, Coverage: map[int]bool{0: false}})
	if graded.CitationValidity.Numerator != 1 || graded.Faithfulness.Numerator != 0 || graded.Groundedness.Numerator != 0 || graded.Coverage.Numerator != 0 {
		t.Fatal(graded)
	}
	p.Claims[0].Evidence[0].SourceID = "invented"
	missing := Evaluate(p, []research.Source{source}, 1, Labels{Claims: map[string]bool{"claim-1": true}})
	if missing.CitationValidity.Numerator != 0 || missing.Groundedness.Numerator != 0 {
		t.Fatal(missing)
	}
	p.Claims = nil
	empty := Evaluate(p, nil, 1, Labels{})
	if empty.Groundedness.Numerator != 0 || empty.Groundedness.Denominator != 1 || empty.Coverage != nil {
		t.Fatal(empty)
	}
}

func TestFrozenFixtureCoverage(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil || len(fixtures) != 12 {
		t.Fatal(len(fixtures), err)
	}
	seen := map[string]bool{}
	for _, f := range fixtures {
		if seen[f.ID] || f.ID == "" || len(f.Pages) == 0 || f.GradingNotes == "" {
			t.Fatal(f.ID)
		}
		seen[f.ID] = true
		if err := (research.Request{WorkID: "eval", Question: f.Question, SuccessCriteria: f.Criteria, AllowDomains: f.AllowDomains, BlockDomains: f.BlockDomains}).Validate(); err != nil {
			t.Fatal(f.ID, err)
		}
	}
}
