// Package eventlog defines session event storage and independent cursor readers.
package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/identity"
	"time"
)

var (
	ErrSealed   = errors.New("event log sealed")
	ErrDisposed = errors.New("event log disposed")
	ErrExpired  = errors.New("event cursor expired")
	ErrFuture   = errors.New("event cursor exceeds latest sequence")
	ErrCapture  = errors.New("event capture failed")
	ErrDetached = errors.New("subscription detached")
)

const SchemaVersion = 2

type Data struct {
	Output      *identity.OutputID `json:"output,omitempty"`
	Message     identity.MessageID `json:"message,omitempty"`
	Correlation string             `json:"correlation,omitempty"`
	Kind        string             `json:"kind"`
	Agent       string             `json:"agent,omitempty"`
	Time        time.Time          `json:"time"
 "github.com/stevemurr/strap/identity"`
	Payload json.RawMessage `json:"payload"`
}

func (d Data) Clone() Data {
	if d.Output != nil {
		v := *d.Output
		d.Output = &v
	}
	d.Payload = append(json.RawMessage(nil), d.Payload...)
	return d
}
func (d Data) Size() int {
	return len(d.Kind) + len(d.Agent) + len(d.Correlation) + len(d.Payload) + 128
}
func (d Data) Validate() error {
	if d.Kind == "" || len(d.Kind) > 128 || len(d.Agent) > 256 || len(d.Correlation) > 256 || !json.Valid(d.Payload) {
		return errors.New("invalid event data")
	}
	return nil
}

type Event struct {
	Schema   int    `json:"schema"`
	Session  string `json:"session"`
	Sequence uint64 `json:"sequence"`
	Data
}

type Record = Event

func (e Event) Cursor() Cursor { return Cursor{Session: e.Session, Sequence: e.Sequence} }

func (e Event) Clone() Event { e.Data = e.Data.Clone(); return e }

type Query struct {
	MaxBytes int    `json:"max_bytes,omitempty"`
	After    uint64 `json:"after"`
	Limit    int    `json:"limit"`
}

func (q Query) Validate() error {
	if q.Limit < 1 || q.Limit > 1000 || q.MaxBytes < 0 {
		return errors.New("event page limit must be between 1 and 1000")
	}
	return nil
}

type Outcome struct {
	Reason          string `json:"reason,omitempty"`
	CleanupError    string `json:"cleanup_error,omitempty"`
	CleanupAttempts uint64 `json:"cleanup_attempts"`
	Error           string `json:"error,omitempty"`
	CaptureError    string `json:"capture_error,omitempty"`
	Omitted         uint64 `json:"omitted"`
}

type Page struct {
	Head     Head     `json:"head"`
	Events   []Event  `json:"events"`
	Next     uint64   `json:"next"`
	Earliest uint64   `json:"earliest"`
	Latest   uint64   `json:"latest"`
	Sealed   bool     `json:"sealed"`
	Outcome  *Outcome `json:"outcome,omitempty"`
}

type Store interface {
	Head(context.Context) (Head, error)
	Wait(context.Context, Cursor) (Head, error)
	Fail(error)
	Append(context.Context, Data) (Event, error)
	Read(context.Context, Query) (Page, error)
	Seal(context.Context, Outcome) error
	Close(context.Context) error
}

type Limits struct {
	Entries int `json:"entries"`
	Bytes   int `json:"bytes"`
}

func (l Limits) Validate() error {
	if l.Entries < 1 || l.Bytes < 4096 {
		return errors.New("event limits require positive entries and at least 4096 bytes")
	}
	return nil
}

func terminal(o Outcome) Data {
	p, _ := json.Marshal(o)
	return Data{Kind: "session_closed", Time: time.Now().UTC(), Payload: p}
}
func cursor(q Query, first, last uint64) error {
	if q.After > last {
		return fmt.Errorf("%w: latest %d", ErrFuture, last)
	}
	if first > 0 && q.After < first-1 {
		return fmt.Errorf("%w: earliest %d", ErrExpired, first)
	}
	return nil
}
