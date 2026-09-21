package vllm

import (
	"fmt"
	"math"
	"reflect"
)

// Generation contains the supported subset of vLLM generation options. Nil
// leaves a field to the server; explicit zero/false is sent unchanged. New
// validates finite numbers and snapshots all values without supplying defaults.
type Generation struct {
	Temperature          *float64 `json:"temperature,omitempty"`            // [0, 2]; zero selects greedy decoding.
	TopP                 *float64 `json:"top_p,omitempty"`                  // (0, 1]
	TopK                 *int     `json:"top_k,omitempty"`                  // -1 or 0 disables filtering; positive values select k.
	MinP                 *float64 `json:"min_p,omitempty"`                  // [0, 1]
	PresencePenalty      *float64 `json:"presence_penalty,omitempty"`       // [-2, 2]
	RepetitionPenalty    *float64 `json:"repetition_penalty,omitempty"`     // > 0; 1 disables the penalty.
	MaxTokens            *int     `json:"max_tokens,omitempty"`             // > 0; output budget, not context length.
	ForceNonemptyContent *bool    `json:"force_nonempty_content,omitempty"` // Requires support in the served chat template.
	EnableThinking       *bool    `json:"enable_thinking,omitempty"`        // Requires support in the served chat template.
	ReasoningEffort      *string  `json:"reasoning_effort,omitempty"`       // Qwen template: low, medium, or xhigh.

}

// Empty reports whether no generation setting was supplied, so a backend with
// no generation policy of its own can reject settings meant for another one.
func (g Generation) Empty() bool { return reflect.DeepEqual(g, Generation{}) }

// Clone returns an independent copy, preserving unset and explicit zero values.
func (g Generation) Clone() Generation {
	g.Temperature = copyValue(g.Temperature)
	g.TopP = copyValue(g.TopP)
	g.TopK = copyValue(g.TopK)
	g.MinP = copyValue(g.MinP)
	g.PresencePenalty = copyValue(g.PresencePenalty)
	g.RepetitionPenalty = copyValue(g.RepetitionPenalty)
	g.MaxTokens = copyValue(g.MaxTokens)
	g.EnableThinking = copyValue(g.EnableThinking)
	g.ReasoningEffort = copyValue(g.ReasoningEffort)
	g.ForceNonemptyContent = copyValue(g.ForceNonemptyContent)
	return g
}

type generationFields struct {
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	TopK              *int            `json:"top_k,omitempty"`
	MinP              *float64        `json:"min_p,omitempty"`
	PresencePenalty   *float64        `json:"presence_penalty,omitempty"`
	RepetitionPenalty *float64        `json:"repetition_penalty,omitempty"`
	MaxTokens         *int            `json:"max_tokens,omitempty"`
	ChatTemplate      *templateFields `json:"chat_template_kwargs,omitempty"`
}

type templateFields struct {
	EnableThinking       *bool   `json:"enable_thinking,omitempty"`
	ForceNonemptyContent *bool   `json:"force_nonempty_content,omitempty"`
	ReasoningEffort      *string `json:"reasoning_effort,omitempty"`
}

func (g Generation) freeze() (generationFields, error) {
	for _, field := range []struct {
		name     string
		value    *float64
		min, max float64
		openMin  bool
	}{
		{"temperature", g.Temperature, 0, 2, false},
		{"top_p", g.TopP, 0, 1, true},
		{"min_p", g.MinP, 0, 1, false},
		{"presence_penalty", g.PresencePenalty, -2, 2, false},
		{"repetition_penalty", g.RepetitionPenalty, 0, math.Inf(1), true},
	} {
		if field.value == nil {
			continue
		}
		v := *field.value
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return generationFields{}, fmt.Errorf("%s must be finite", field.name)
		}
		if v < field.min || v > field.max || (field.openMin && v == field.min) {
			left := "["
			if field.openMin {
				left = "("
			}
			return generationFields{}, fmt.Errorf("%s must be in %s%g, %g]", field.name, left, field.min, field.max)
		}
	}
	if g.TopK != nil && *g.TopK < -1 {
		return generationFields{}, fmt.Errorf("top_k must be -1, 0, or a positive integer")
	}
	if g.MaxTokens != nil && *g.MaxTokens < 1 {
		return generationFields{}, fmt.Errorf("max_tokens must be a positive integer")
	}
	if g.ReasoningEffort != nil {
		switch *g.ReasoningEffort {
		case "low", "medium", "xhigh":
		default:
			return generationFields{}, fmt.Errorf("reasoning_effort must be low, medium, or xhigh")
		}
	}
	g = g.Clone()
	fields := generationFields{
		Temperature: g.Temperature, TopP: g.TopP, TopK: g.TopK,
		MinP: g.MinP, PresencePenalty: g.PresencePenalty,
		RepetitionPenalty: g.RepetitionPenalty, MaxTokens: g.MaxTokens,
	}
	if g.EnableThinking != nil || g.ForceNonemptyContent != nil || g.ReasoningEffort != nil {
		fields.ChatTemplate = &templateFields{EnableThinking: g.EnableThinking, ForceNonemptyContent: g.ForceNonemptyContent, ReasoningEffort: g.ReasoningEffort}
	}
	return fields, nil
}

func copyValue[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
