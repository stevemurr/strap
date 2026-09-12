// Package identity defines actor identity independently of transport and work.
package identity

type ActorID string

type SessionID string
type MessageID string
type ToolInvocationID string
type ContentID string
type OutputID struct {
	Agent ActorID `json:"agent"`
	Call  uint64  `json:"call"`
}
