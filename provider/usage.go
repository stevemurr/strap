package provider

// Usage reports tokens for one model call, including tool-related content.
// Counts must be nonnegative. Nil means unavailable; zero is a reported count.
// These are provider measurements, not estimates of visible text or the next
// request. Total tokens can be derived when both counts are available.
type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	// CachedTokens is the part of InputTokens served from the server's
	// prefix cache (vLLM reports it with --enable-prompt-tokens-details).
	CachedTokens *int64 `json:"cached_tokens,omitempty"`
	// ReasoningTokens is the part of OutputTokens spent thinking.
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
}

// Clone returns an independent snapshot and is safe to call on nil.
func (u *Usage) Clone() *Usage {
	if u == nil {
		return nil
	}
	copy := *u
	if u.InputTokens != nil {
		v := *u.InputTokens
		copy.InputTokens = &v
	}
	if u.OutputTokens != nil {
		v := *u.OutputTokens
		copy.OutputTokens = &v
	}
	if u.CachedTokens != nil {
		v := *u.CachedTokens
		copy.CachedTokens = &v
	}
	if u.ReasoningTokens != nil {
		v := *u.ReasoningTokens
		copy.ReasoningTokens = &v
	}
	return &copy
}
