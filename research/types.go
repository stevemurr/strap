// Package research runs bounded investigations without agent or workflow ownership.
package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const Version = 2
const PromptVersion = "deep-research-v2"

var (
	ErrBusy       = errors.New("a deep research run is already active")
	ErrTokens     = errors.New("research token budget exhausted")
	ErrRequests   = errors.New("research request budget exhausted")
	ErrRetention  = errors.New("research retention budget exhausted")
	ErrOutput     = errors.New("research model output limit exceeded")
	ErrModel      = errors.New("invalid research model output")
	ErrReassigned = errors.New("research assignment retired")
)

type Request struct {
	WorkID          string   `json:"work_id"`
	Question        string   `json:"question"`
	Context         *string  `json:"context"`
	SuccessCriteria []string `json:"success_criteria"`
	MustCover       []string `json:"must_cover"`
	AllowDomains    []string `json:"allow_domains"`
	BlockDomains    []string `json:"block_domains"`
	Depth           *string  `json:"depth"`
	MaxMinutes      *int64   `json:"max_minutes"`
	MaxTokens       *int64   `json:"max_tokens"`
}

type Binding struct {
	WorkID       string `json:"work_id"`
	Assignment   uint64 `json:"assigned_at_revision"`
	Actor        string `json:"actor"`
	InvocationID string `json:"invocation_id"`
}

type Hit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}
type Page struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Title       string `json:"title"`
	ContentType string `json:"content_type"`
	Text        string `json:"text"`
	Truncated   bool   `json:"document_truncated"`
	Links       []Hit  `json:"links,omitempty"`
}

// Retrieval implementations must honor cancellation and bound returned data.
type Retrieval interface {
	Search(context.Context, string) ([]Hit, error)
	Fetch(context.Context, string) (Page, error)
}

type Source struct {
	ID          string    `json:"source_id"`
	URL         string    `json:"url"`
	FinalURL    string    `json:"final_url"`
	Title       string    `json:"title"`
	ContentType string    `json:"content_type"`
	FetchedAt   time.Time `json:"fetched_at"`
	SHA256      string    `json:"sha256"`
	Text        string    `json:"text,omitempty"`
	Bytes       int       `json:"bytes"`
	Truncated   bool      `json:"document_truncated"`
}

type Citation struct {
	SourceID string `json:"source_id"`
	Quote    string `json:"quote"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	URI      string `json:"uri"`
	Revision string `json:"revision"`
	Locator  string `json:"locator"`
}

type Claim struct {
	ID         string     `json:"claim_id"`
	Claim      string     `json:"claim"`
	Basis      string     `json:"basis"`
	Evidence   []Citation `json:"evidence"`
	Limitation string     `json:"limitation"`
	Verdict    string     `json:"verification"`
	Reason     string     `json:"verification_reason,omitempty"`
}

type Coverage struct {
	Index       int      `json:"index"`
	Requirement string   `json:"requirement"`
	Status      string   `json:"status"`
	ClaimIDs    []string `json:"claim_ids"`
	Reason      string   `json:"reason"`
}

type Spend struct {
	ModelCalls      int           `json:"model_calls"`
	Searches        int           `json:"search_attempts"`
	Fetches         int           `json:"fetch_attempts"`
	SearchSuccesses int           `json:"search_successes"`
	FetchSuccesses  int           `json:"fetch_successes"`
	InputTokens     int64         `json:"input_tokens"`
	OutputTokens    int64         `json:"output_tokens"`
	MissingInput    int           `json:"missing_input_calls"`
	MissingOutput   int           `json:"missing_output_calls"`
	ChargedTokens   int64         `json:"charged_tokens"`
	SourceBytes     int           `json:"source_bytes"`
	Elapsed         time.Duration `json:"elapsed_ns"`
}

type Report struct {
	ID             string     `json:"run_id"`
	Version        int        `json:"schema_version"`
	PromptVersion  string     `json:"prompt_version"`
	Binding        Binding    `json:"binding"`
	Question       string     `json:"question"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     time.Time  `json:"finished_at"`
	Status         string     `json:"status"`
	StopReason     string     `json:"stop_reason"`
	Summary        string     `json:"summary"`
	Claims         []Claim    `json:"claims"`
	Rejected       []Claim    `json:"rejected_claims,omitempty"`
	Disagreements  [][]string `json:"disagreements,omitempty"`
	OpenQuestions  []string   `json:"open_questions,omitempty"`
	Limitations    []string   `json:"limitations,omitempty"`
	Recommendation string     `json:"recommendation,omitempty"`
	Coverage       []Coverage `json:"coverage"`
	Sources        []Source   `json:"sources"`
	Limits         Limits     `json:"limits"`
	Spend          Spend      `json:"spend"`
	Truncated      bool       `json:"truncated"`
}

// Event is a required, immutable record. Recorders must acknowledge retention.
// Source text is recorded once; checkpoints/final reports contain manifests only.
type Event struct {
	RunID     string      `json:"run_id"`
	Binding   Binding     `json:"binding"`
	Sequence  uint64      `json:"sequence"`
	Kind      string      `json:"kind"` // started, progress, source, usage, checkpoint, finished
	At        time.Time   `json:"at"`
	Stage     string      `json:"stage,omitempty"`
	Scout     int         `json:"scout,omitempty"`
	Completed uint64      `json:"completed_steps"`
	Deadline  time.Time   `json:"deadline"`
	Source    *Source     `json:"source,omitempty"`
	Report    *Report     `json:"report,omitempty"`
	Spend     *Spend      `json:"spend,omitempty"`
	Call      *CallRecord `json:"call,omitempty"`
}

type CallRecord struct {
	Number       int       `json:"number"`
	Stage        string    `json:"stage"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	InputTokens  *int64    `json:"input_tokens"`
	OutputTokens *int64    `json:"output_tokens"`
	Error        string    `json:"error,omitempty"`
}

type Recorder func(context.Context, Event) error

func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.Clone(s[:n])
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }

func (r Request) Validate() error {
	b, err := json.Marshal(r)
	if err != nil || len(b) > 32<<10 || strings.TrimSpace(r.WorkID) == "" || strings.TrimSpace(r.Question) == "" || !utf8.Valid(b) {
		return errors.New("research requires work_id, question and at most 32 KiB of input")
	}
	if len(r.SuccessCriteria) < 1 || len(r.SuccessCriteria) > 8 || len(r.MustCover) > 12 || len(r.AllowDomains) > 32 || len(r.BlockDomains) > 32 {
		return errors.New("research requires 1..8 criteria, at most 12 topics and 32 domains per filter")
	}
	for _, s := range append(append([]string{}, r.SuccessCriteria...), r.MustCover...) {
		if strings.TrimSpace(s) == "" {
			return errors.New("research requirements cannot be blank")
		}
	}
	if r.Depth != nil && *r.Depth != "survey" && *r.Depth != "standard" && *r.Depth != "exhaustive" {
		return errors.New("depth must be survey, standard or exhaustive")
	}
	if r.MaxMinutes != nil && (*r.MaxMinutes < 1 || *r.MaxMinutes > 20) {
		return errors.New("max_minutes must be 1..20")
	}
	if r.MaxTokens != nil && (*r.MaxTokens < 1 || *r.MaxTokens > 1_000_000_000) {
		return errors.New("max_tokens must be 1..1000000000")
	}
	_, err = newPolicy(r.AllowDomains, r.BlockDomains)
	return err
}

func (e Event) Validate() error {
	if err := e.ValidateControl(); err != nil {
		return err
	}
	if e.Kind == "source" && (!utf8.ValidString(e.Source.Text) || e.Source.Bytes != len(e.Source.Text) || hash(e.Source.Text) != e.Source.SHA256) {
		return errors.New("invalid research source content")
	}
	return nil
}

// ValidateControl also applies to content-framed records with source text omitted.
func (e Event) ValidateControl() error {
	if e.RunID == "" || e.Binding.Actor == "" || e.Binding.WorkID == "" || e.Binding.Assignment == 0 || e.Binding.InvocationID == "" || e.Sequence == 0 || e.At.IsZero() || e.Deadline.IsZero() {
		return errors.New("invalid research record identity")
	}
	if e.Source != nil && e.Kind != "source" || e.Report != nil && e.Kind != "started" && e.Kind != "checkpoint" && e.Kind != "finished" || e.Call != nil && e.Kind != "usage" && e.Kind != "progress" {
		return errors.New("unexpected research record payload")
	}
	switch e.Kind {
	case "started", "checkpoint", "finished":
		if e.Report == nil || e.Report.ID != e.RunID || e.Report.Binding != e.Binding || e.Report.Version != Version || e.Report.StartedAt.IsZero() {
			return errors.New("invalid research report record")
		}
		if e.Kind == "finished" && (e.Report.FinishedAt.IsZero() || (e.Report.Status != "complete" && e.Report.Status != "partial" && e.Report.Status != "failed")) {
			return errors.New("invalid research finish")
		}
		if e.Kind != "finished" && (!e.Report.FinishedAt.IsZero() || e.Report.Status != "running" && e.Report.Status != "partial") {
			return errors.New("invalid research checkpoint")
		}
	case "source":
		if e.Source == nil || e.Source.ID == "" || e.Source.Bytes < 1 || len(e.Source.SHA256) != 64 || e.Source.FetchedAt.IsZero() {
			return errors.New("invalid research source")
		}
	case "usage":
		if e.Call == nil || e.Call.Number < 1 || e.Call.FinishedAt.IsZero() || e.Spend == nil {
			return errors.New("invalid research usage")
		}
	case "progress":
		if e.Stage == "" {
			return errors.New("research progress requires stage")
		}
	default:
		return fmt.Errorf("unknown research event %q", e.Kind)
	}
	return nil
}
