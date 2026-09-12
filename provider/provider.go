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
	Submit(context.Context, Request, Observer) (Response, error)
}

type Request struct {
	Agent    message.ActorID  `json:"agent"`
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools"`
}

// Message is model history. Envelope preserves who actually sent inbox input.
// Role is system, user, assistant, or tool.
type Message struct {
	Role       string           `json:"role"`
	Content    content.Content  `json:"content"`
	Envelope   *message.Message `json:"envelope"`
	ToolCalls  []ToolCall       `json:"tool_calls"`
	ToolCallID string           `json:"tool_call_id"`
}

type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls"`
	Usage     *Usage     `json:"usage"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
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

// Delta is an append-only, valid UTF-8 text prefix. Callbacks are serial.
type Delta struct {
	Text string `json:"text"`
}
type Observer interface{ OnDelta(Delta) error }
type ObserverFunc func(Delta) error

func (f ObserverFunc) OnDelta(d Delta) error { return f(d) }
