package chatwire

import "testing"

// vLLM reports reasoning tokens in completion_tokens_details, and cached
// prompt tokens in prompt_tokens_details when --enable-prompt-tokens-details
// is set; each detail is optional and a malformed one is only unavailable.
func TestDecodeUsageReadsTokenDetails(t *testing.T) {
	u := decodeUsage([]byte(`{"prompt_tokens":1218,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens_details":{"reasoning_tokens":31}}`))
	if u == nil || *u.InputTokens != 1218 || *u.OutputTokens != 40 || *u.CachedTokens != 1024 || *u.ReasoningTokens != 31 {
		t.Fatalf("%+v", u)
	}
	u = decodeUsage([]byte(`{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":-1}}`))
	if u == nil || u.CachedTokens != nil || u.ReasoningTokens != nil || *u.InputTokens != 10 {
		t.Fatalf("%+v", u)
	}
	if c := u.Clone(); c == u || *c.InputTokens != 10 {
		t.Fatal("clone aliases")
	}
}
