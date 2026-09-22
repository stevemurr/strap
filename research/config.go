package research

import (
	"errors"
	"time"
)

type Limits struct {
	Duration     time.Duration `json:"duration_ns"`
	Scouts       int           `json:"scouts"`
	Subquestions int           `json:"subquestions"`
	Searches     int           `json:"searches"`
	Fetches      int           `json:"fetches"`
	ModelCalls   int           `json:"model_calls"`
	Rounds       int           `json:"followup_rounds"`
	Tokens       int64         `json:"tokens"` // Zero uses request/time limits only.
}

type Config struct {
	Survey            Limits        `json:"survey"`
	Standard          Limits        `json:"standard"`
	Exhaustive        Limits        `json:"exhaustive"`
	MaxDuration       time.Duration `json:"max_duration_ns"`
	ModelConcurrency  int           `json:"model_concurrency"`
	MaxReportBytes    int           `json:"max_report_bytes"`
	MaxSourceBytes    int           `json:"max_source_bytes"`
	MaxRunSourceBytes int           `json:"max_run_source_bytes"`
	MaxSessionBytes   int           `json:"max_session_bytes"`
	MaxRequestBytes   int           `json:"max_request_bytes"`
	MaxOutputBytes    int           `json:"max_output_bytes"`
	MaxReasoningBytes int           `json:"max_reasoning_bytes"`
	SettlementTimeout time.Duration `json:"settlement_timeout_ns"`
}

func DefaultConfig() Config {
	return Config{
		Survey:      Limits{Duration: 2 * time.Minute, Scouts: 1, Subquestions: 3, Searches: 8, Fetches: 12, ModelCalls: 16, Rounds: 1},
		Standard:    Limits{Duration: 6 * time.Minute, Scouts: 2, Subquestions: 6, Searches: 20, Fetches: 30, ModelCalls: 40, Rounds: 2},
		Exhaustive:  Limits{Duration: 15 * time.Minute, Scouts: 4, Subquestions: 8, Searches: 40, Fetches: 60, ModelCalls: 80, Rounds: 3},
		MaxDuration: 20 * time.Minute, ModelConcurrency: 2, MaxReportBytes: 256 << 10, MaxSourceBytes: 512 << 10, MaxRunSourceBytes: 16 << 20, MaxSessionBytes: 64 << 20,
		MaxRequestBytes: 128 << 10, MaxOutputBytes: 64 << 10, MaxReasoningBytes: 192 << 10, SettlementTimeout: 5 * time.Second,
	}
}
func (c Config) Resolve() (Config, error) {
	d := DefaultConfig()
	if c.Survey == (Limits{}) {
		c.Survey = d.Survey
	}
	if c.Standard == (Limits{}) {
		c.Standard = d.Standard
	}
	if c.Exhaustive == (Limits{}) {
		c.Exhaustive = d.Exhaustive
	}
	if c.MaxDuration == 0 {
		c.MaxDuration = d.MaxDuration
	}
	if c.ModelConcurrency == 0 {
		c.ModelConcurrency = d.ModelConcurrency
	}
	if c.MaxReportBytes == 0 {
		c.MaxReportBytes = d.MaxReportBytes
	}
	if c.MaxSourceBytes == 0 {
		c.MaxSourceBytes = d.MaxSourceBytes
	}
	if c.MaxRunSourceBytes == 0 {
		c.MaxRunSourceBytes = d.MaxRunSourceBytes
	}
	if c.MaxSessionBytes == 0 {
		c.MaxSessionBytes = d.MaxSessionBytes
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = d.MaxRequestBytes
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = d.MaxOutputBytes
	}
	if c.MaxReasoningBytes == 0 {
		c.MaxReasoningBytes = d.MaxReasoningBytes
	}
	if c.SettlementTimeout == 0 {
		c.SettlementTimeout = d.SettlementTimeout
	}
	if c.MaxDuration <= 0 || c.MaxDuration > 20*time.Minute || c.ModelConcurrency < 1 || c.ModelConcurrency > 4 || c.MaxReportBytes < 4096 || c.MaxReportBytes > 256<<10 || c.MaxSourceBytes < 200 || c.MaxSourceBytes > 512<<10 || c.MaxRunSourceBytes < c.MaxSourceBytes || c.MaxRunSourceBytes > 16<<20 || c.MaxSessionBytes < 2*c.MaxReportBytes || c.MaxSessionBytes > 1<<30 || c.MaxRequestBytes < 4096 || c.MaxRequestBytes > 1<<20 || c.MaxOutputBytes < 1024 || c.MaxOutputBytes > 256<<10 || c.MaxReasoningBytes < 1024 || c.MaxReasoningBytes > 1<<20 || c.SettlementTimeout <= 0 || c.SettlementTimeout > 30*time.Second {
		return c, errors.New("invalid deep research resource limits")
	}
	for _, l := range []Limits{c.Survey, c.Standard, c.Exhaustive} {
		if l.Duration <= 0 || l.Duration > c.MaxDuration || l.Scouts < 1 || l.Scouts > 4 || l.Subquestions < 1 || l.Subquestions > 8 || l.Searches < 1 || l.Searches > 100 || l.Fetches < 1 || l.Fetches > 100 || l.ModelCalls < 4 || l.ModelCalls > 200 || l.Rounds < 0 || l.Rounds > 3 || l.Tokens < 0 || l.Tokens > 1_000_000_000 {
			return c, errors.New("invalid research preset")
		}
	}
	return c, nil
}
func (c Config) limits(r Request) Limits {
	l := c.Standard
	if r.Depth != nil {
		if *r.Depth == "survey" {
			l = c.Survey
		}
		if *r.Depth == "exhaustive" {
			l = c.Exhaustive
		}
	}
	if r.MaxMinutes != nil {
		l.Duration = min(l.Duration, time.Duration(*r.MaxMinutes)*time.Minute)
	}
	if r.MaxTokens != nil && (l.Tokens == 0 || *r.MaxTokens < l.Tokens) {
		l.Tokens = *r.MaxTokens
	}
	return l
}
