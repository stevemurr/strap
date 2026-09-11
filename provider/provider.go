// Package provider defines the provider request/response boundary.
package provider

import (
	"context"
	"encoding/json"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
)

// Provider submits requests to a configured model server. Implementations may be called concurrently by
// different agents. A response contains provider output, not lifecycle decisions.
type Provider interface {
	// Submit may return Usage alongside an error. Callers may record that usage,
	// but must not consume Content or ToolCalls when err is non-nil.
	Submit(context.Context, Request) (Response, error)
}

type Request struct {
	Agent    message.ActorID
	Messages []Message
	Tools    []ToolDefinition
}

// Message is model history. Envelope preserves who actually sent inbox input.
// Role is system, user, assistant, or tool.
type Message struct {
	Role       string
	Content    content.Content
	Envelope   *message.Message
	ToolCalls  []ToolCall
	ToolCallID string
}

type Response struct {
	Content   string
	ToolCalls []ToolCall
	Usage     *Usage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

func CopyMessages(in []Message) []Message {
	out := append([]Message(nil), in...)
	for i := range out {
		out[i].Content = out[i].Content.Clone()
		out[i].ToolCalls = CopyCalls(out[i].ToolCalls)
		if out[i].Envelope != nil {
			envelope := out[i].Envelope.Clone()
			out[i].Envelope = &envelope
		}
	}
	return out
}

func CopyCalls(in []ToolCall) []ToolCall {
	out := append([]ToolCall(nil), in...)
	for i := range out {
		out[i].Arguments = append(json.RawMessage(nil), out[i].Arguments...)
	}
	return out
}
