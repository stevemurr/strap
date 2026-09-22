package deepresearch

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/research"
)

//go:embed testdata/questions.json
var fixtureJSON []byte

type Fixture struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Question     string          `json:"question"`
	Criteria     []string        `json:"criteria"`
	AllowDomains []string        `json:"allow_domains"`
	BlockDomains []string        `json:"block_domains"`
	Pages        []research.Page `json:"pages"`
	GradingNotes string          `json:"grading_notes"`
}

func Fixtures() ([]Fixture, error) {
	var fixtures []Fixture
	err := json.Unmarshal(fixtureJSON, &fixtures)
	return fixtures, err
}

// Corpus returns the same frozen leads for any query. This isolates investigation
// and verification quality from search ranking; it does not measure web recall.
type Corpus struct{ Pages []research.Page }

func (c Corpus) Search(ctx context.Context, _ string) ([]research.Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var hits []research.Hit
	for _, page := range c.Pages {
		hits = append(hits, research.Hit{URL: page.FinalURL, Title: page.Title})
	}
	return hits, nil
}
func (c Corpus) Fetch(ctx context.Context, url string) (research.Page, error) {
	if err := ctx.Err(); err != nil {
		return research.Page{}, err
	}
	for _, page := range c.Pages {
		if page.FinalURL == url {
			return page, nil
		}
	}
	return research.Page{}, fmt.Errorf("URL is outside the frozen corpus: %s", url)
}
