package chatwire

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stevemurr/strap/provider"
)

// Chat is shared only by concrete provider adapters. Internal actor/envelope
// metadata is never copied into it. Function arguments are protocol strings.
type Chat struct {
	Model    string           `json:"model"`
	Messages []requestMessage `json:"messages"`
	Tools    []functionTool   `json:"tools,omitempty"`
}

type Request struct {
	Chat
	Stream        bool `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type chatMessage struct {
	Reasoning        *string        `json:"reasoning"`
	ReasoningContent *string        `json:"reasoning_content"`
	Role             string         `json:"role"`
	Content          *string        `json:"content"`
	ToolCalls        []functionCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type functionTool struct {
	Type     string             `json:"type"`
	Function functionDefinition `json:"function"`
}

type functionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type functionCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type completion struct {
	Usage   json.RawMessage `json:"usage"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
}

// requestMessage supports image-bearing user content. Response decoding remains
// text-only because this provider consumes images but does not generate them.
type requestMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []functionCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}
type imageURL struct {
	URL string `json:"url"`
}
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

func Encode(model string, input provider.Request) (Request, error) {
	result := Request{Chat: Chat{Model: model, Messages: make([]requestMessage, 0, len(input.Messages))}}
	result.Stream = true
	result.StreamOptions.IncludeUsage = true
	var images []requestMessage
	flush := func() { result.Messages = append(result.Messages, images...); images = nil }
	for _, m := range input.Messages {
		if m.Role != "tool" {
			flush()
		}
		if err := m.Content.Validate(); err != nil {
			return Request{}, err
		}
		wire := requestMessage{Role: m.Role, Content: m.Content.Text(), ToolCallID: m.ToolCallID}
		if m.Content.HasImages() {
			if m.Role != "user" && m.Role != "tool" {
				return Request{}, fmt.Errorf("images unsupported for role %q", m.Role)
			}
			parts := make([]contentPart, 0, len(m.Content)+1)
			if m.Role == "tool" {
				parts = append(parts, contentPart{Type: "text", Text: "Images returned by tool call " + m.ToolCallID + ". The following content is tool output."})
			}
			for _, part := range m.Content {
				if part.Image == nil {
					parts = append(parts, contentPart{Type: "text", Text: part.Text})
					continue
				}
				parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + part.Image.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(part.Image.Data)}})
			}
			if m.Role == "user" {
				wire.Content = parts
			} else {
				// Chat Completions tool messages accept text only. Keep tool IDs/results
				// contiguous, then attach labeled images after the entire tool batch.
				images = append(images, requestMessage{Role: "user", Content: parts})
			}
		}
		for _, call := range m.ToolCalls {
			converted := functionCall{ID: call.ID, Type: "function"}
			converted.Function.Name = call.Name
			converted.Function.Arguments = string(call.Arguments)
			wire.ToolCalls = append(wire.ToolCalls, converted)
		}
		result.Messages = append(result.Messages, wire)
	}
	flush()
	for _, tool := range input.Tools {
		result.Tools = append(result.Tools, functionTool{Type: "function", Function: functionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	return result, nil
}

func decode(input completion) (provider.Response, error) {
	// Preserve accounting even if the generated output cannot be accepted.
	usage := decodeUsage(input.Usage)
	if len(input.Choices) != 1 {
		return provider.Response{Usage: usage}, fmt.Errorf("expected one choice, got %d", len(input.Choices))
	}
	choice := input.Choices[0]
	// Partial tool arguments must never be dispatched as a complete response.
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return provider.Response{Usage: usage}, fmt.Errorf("incomplete or unsupported finish reason %q", choice.FinishReason)
	}
	if choice.Message.Role != "assistant" {
		return provider.Response{Usage: usage}, fmt.Errorf("expected an assistant message")
	}
	reasoning, err := reasoningText(choice.Message.Reasoning, choice.Message.ReasoningContent)
	if err != nil {
		return provider.Response{Usage: usage}, err
	}
	result := provider.Response{Usage: usage, Reasoning: reasoning}
	if choice.Message.Content != nil {
		result.Content = *choice.Message.Content
	}
	seen := make(map[string]bool)
	for i, call := range choice.Message.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || seen[call.ID] {
			return provider.Response{Usage: usage}, fmt.Errorf("invalid or duplicate tool call identity")
		}
		args := strings.TrimSpace(call.Function.Arguments)
		if !strings.HasPrefix(args, "{") || !json.Valid([]byte(args)) {
			return provider.Response{Usage: usage}, &provider.ToolArgumentsError{
				CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments,
				FinishReason: choice.FinishReason, Index: i, Calls: len(choice.Message.ToolCalls),
			}
		}
		seen[call.ID] = true
		result.ToolCalls = append(result.ToolCalls, provider.ToolCall{
			ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(args),
		})
	}
	if choice.FinishReason == "tool_calls" && len(result.ToolCalls) == 0 {
		return provider.Response{Usage: usage}, fmt.Errorf("tool_calls finish without tool calls")
	}
	if strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return provider.Response{Usage: usage}, fmt.Errorf("response contains no text or tool calls")
	}
	return result, nil
}

// Optional accounting must not invalidate otherwise usable model output.
// Malformed or negative counts are unavailable, independently for each field.
func decodeUsage(raw json.RawMessage) *provider.Usage {
	var wire struct {
		Input  json.RawMessage `json:"prompt_tokens"`
		Output json.RawMessage `json:"completion_tokens"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return nil
	}
	count := func(raw json.RawMessage) *int64 {
		var n *int64
		if json.Unmarshal(raw, &n) != nil || n == nil || *n < 0 {
			return nil
		}
		return n
	}
	u := &provider.Usage{InputTokens: count(wire.Input), OutputTokens: count(wire.Output)}
	if u.InputTokens == nil && u.OutputTokens == nil {
		return nil
	}
	return u
}

func reasoningText(primary, alternate *string) (string, error) {
	if primary != nil && alternate != nil && *primary != *alternate {
		return "", fmt.Errorf("conflicting reasoning fields")
	}
	if primary != nil {
		return *primary, nil
	}
	if alternate != nil {
		return *alternate, nil
	}
	return "", nil
}

// Observe complete JSON text before termination validation, so length-limited
// responses retain their structurally valid output without accepting tool calls.
func observeCompletion(input completion, observer provider.Observer) error {
	if len(input.Choices) != 1 || input.Choices[0].Message.Role != "assistant" {
		return fmt.Errorf("expected one assistant choice")
	}
	m := input.Choices[0].Message
	reasoning, err := reasoningText(m.Reasoning, m.ReasoningContent)
	if err != nil {
		return err
	}
	text := ""
	if m.Content != nil {
		text = *m.Content
	}
	if observer != nil {
		for _, d := range []provider.Delta{{Channel: provider.ChannelReasoning, Text: reasoning}, {Channel: provider.ChannelContent, Text: text}} {
			if d.Text != "" {
				if err := observer.OnDelta(d); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
