package chatcompletions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stevemurr/strap/provider"
)

// Keep wire types private: internal actor/envelope metadata must not become
// unsupported API fields, and function arguments are strings on this protocol.
type request struct {
	Model    string           `json:"model"`
	Messages []requestMessage `json:"messages"`
	Tools    []functionTool   `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []functionCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
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

func encode(model string, input provider.Request) (request, error) {
	result := request{Model: model, Messages: make([]requestMessage, 0, len(input.Messages))}
	var images []requestMessage
	flush := func() { result.Messages = append(result.Messages, images...); images = nil }
	for _, m := range input.Messages {
		if m.Role != "tool" {
			flush()
		}
		if err := m.Content.Validate(); err != nil {
			return request{}, err
		}
		wire := requestMessage{Role: m.Role, Content: m.Content.Text(), ToolCallID: m.ToolCallID}
		if m.Content.HasImages() {
			if m.Role != "user" && m.Role != "tool" {
				return request{}, fmt.Errorf("images unsupported for role %q", m.Role)
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
	if len(input.Choices) != 1 {
		return provider.Response{}, fmt.Errorf("chatcompletions: expected one choice, got %d", len(input.Choices))
	}
	choice := input.Choices[0]
	// Partial tool arguments must never be dispatched as a complete response.
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return provider.Response{}, fmt.Errorf("chatcompletions: incomplete or unsupported finish reason %q", choice.FinishReason)
	}
	if choice.Message.Role != "assistant" {
		return provider.Response{}, fmt.Errorf("chatcompletions: expected an assistant message")
	}
	result := provider.Response{}
	if choice.Message.Content != nil {
		result.Content = *choice.Message.Content
	}
	seen := make(map[string]bool)
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || seen[call.ID] {
			return provider.Response{}, fmt.Errorf("chatcompletions: invalid or duplicate tool call identity")
		}
		args := strings.TrimSpace(call.Function.Arguments)
		if !strings.HasPrefix(args, "{") || !json.Valid([]byte(args)) {
			return provider.Response{}, fmt.Errorf("chatcompletions: tool %s arguments must be a JSON object", call.Function.Name)
		}
		seen[call.ID] = true
		result.ToolCalls = append(result.ToolCalls, provider.ToolCall{
			ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(args),
		})
	}
	if choice.FinishReason == "tool_calls" && len(result.ToolCalls) == 0 {
		return provider.Response{}, fmt.Errorf("chatcompletions: tool_calls finish without tool calls")
	}
	if strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return provider.Response{}, fmt.Errorf("chatcompletions: response contains no text or tool calls")
	}
	return result, nil
}
