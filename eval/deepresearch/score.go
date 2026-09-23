// Package deepresearch evaluates retained research separately from its verifier.
package deepresearch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/stevemurr/strap/research"
)

// Labels must come from a human or independent judge, never Report's verdicts.
// Claim keys are claim IDs; coverage keys are the original requirement indexes.
type Labels struct {
	Claims   map[string]bool `json:"claims"`
	Coverage map[int]bool    `json:"coverage"`
	Useful   *bool           `json:"useful"`
}

type Ratio struct {
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
}

type Score struct {
	CitationValidity Ratio          `json:"citation_validity"`
	ClaimLabels      Ratio          `json:"claim_labels"`
	Faithfulness     *Ratio         `json:"faithfulness"`
	Groundedness     *Ratio         `json:"groundedness"`
	Coverage         *Ratio         `json:"coverage"`
	Useful           *bool          `json:"useful"`
	Spend            research.Spend `json:"spend"`
	Problems         []string       `json:"problems"`
}

// Evaluate checks exact UTF-8 source spans and snapshot hashes. Semantic metrics
// stay null until every relevant item has an independent label. Empty reports
// score 0/1 on claim metrics; abstention cannot earn perfect groundedness.
func Evaluate(report research.Report, sources []research.Source, requirements int, labels Labels) Score {
	s := Score{Spend: report.Spend, Useful: labels.Useful}
	byID := map[string]research.Source{}
	for _, src := range sources {
		byID[src.ID] = src
	}
	faithful, grounded := 0, 0
	for _, f := range report.Claims {
		allValid := len(f.Evidence) > 0
		for _, c := range f.Evidence {
			s.CitationValidity.Denominator++
			src, ok := byID[c.SourceID]
			digest := sha256.Sum256([]byte(src.Text))
			valid := ok && src.SHA256 == hex.EncodeToString(digest[:]) && src.Bytes == len(src.Text) &&
				utf8.ValidString(src.Text) && c.Start >= 0 && c.End > c.Start && c.End <= len(src.Text) &&
				utf8.ValidString(src.Text[:c.Start]) && utf8.ValidString(src.Text[:c.End]) &&
				src.Text[c.Start:c.End] == c.Quote && c.URI == src.FinalURL && c.Revision == "sha256:"+src.SHA256 &&
				c.Locator == fmt.Sprintf("%s/%s#bytes=%d-%d", report.ID, src.ID, c.Start, c.End)
			if valid {
				s.CitationValidity.Numerator++
			} else {
				allValid = false
				s.Problems = append(s.Problems, "Invalid citation in "+f.ID)
			}
		}
		if len(f.Evidence) == 0 {
			s.Problems = append(s.Problems, "No evidence for "+f.ID)
		}
		if supports, ok := labels.Claims[f.ID]; ok {
			s.ClaimLabels.Numerator++
			if supports {
				faithful++
				if allValid {
					grounded++
				}
			}
		}
	}
	s.ClaimLabels.Denominator = len(report.Claims)
	if s.ClaimLabels.Numerator == len(report.Claims) {
		s.Faithfulness = &Ratio{faithful, max(1, len(report.Claims))}
		s.Groundedness = &Ratio{grounded, max(1, len(report.Claims))}
	}
	covered, labeled := 0, 0
	for i := 0; i < requirements; i++ {
		if met, ok := labels.Coverage[i]; ok {
			labeled++
			if met {
				covered++
			}
		}
	}
	if requirements > 0 && labeled == requirements {
		s.Coverage = &Ratio{covered, requirements}
	}
	return s
}
