package provider

// OutputTokenLimiter optionally exposes the configured output cap per Submit.
// It must return promptly without I/O. Nil means the cap is unknown (for example,
// a server default); a known cap is positive. It is not a lifetime usage budget.
type OutputTokenLimiter interface {
	OutputTokenLimit() *int64
}
