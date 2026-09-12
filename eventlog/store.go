// Package eventlog defines session event storage and independent cursor readers.
package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

const SchemaVersion = 1

type Data struct {
	Kind    string          `json:"kind"`
	Agent   string          `json:"agent,omitempty"`
	Time    time.Time       `json:"time"`
	Payload json.RawMessage `json:"payload"`
}

func (d Data) Clone() Data { d.Payload = append(json.RawMessage(nil), d.Payload...); return d }
func (d Data) Size() int   { return len(d.Kind) + len(d.Agent) + len(d.Payload) + 128 }
func (d Data) Validate() error {
	if d.Kind == "" || len(d.Kind) > 128 || len(d.Agent) > 256 || !json.Valid(d.Payload) {
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

func (e Event) Clone() Event { e.Data = e.Data.Clone(); return e }

type Query struct {
	After uint64 `json:"after"`
	Limit int    `json:"limit"`
}

func (q Query) Validate() error {
	if q.Limit < 1 || q.Limit > 1000 {
		return errors.New("event page limit must be between 1 and 1000")
	}
	return nil
}

type Outcome struct {
	Error        string `json:"error,omitempty"`
	CaptureError string `json:"capture_error,omitempty"`
	Omitted      uint64 `json:"omitted"`
}

type Page struct {
	Events   []Event  `json:"events"`
	Next     uint64   `json:"next"`
	Earliest uint64   `json:"earliest"`
	Latest   uint64   `json:"latest"`
	Sealed   bool     `json:"sealed"`
	Outcome  *Outcome `json:"outcome,omitempty"`
}

type Store interface {
	Append(context.Context, Data) (Event, error)
	Read(context.Context, Query) (Page, error)
	Seal(context.Context, Outcome) error
	Close(context.Context) error
}

type Limits struct {
	Entries int
	Bytes   int
}

func (l Limits) Validate() error {
	if l.Entries < 1 || l.Bytes < 4096 {
		return errors.New("event limits require positive entries and at least 4096 bytes")
	}
	return nil
}

// Fit replaces a payload that cannot fit with an explicit, correlated omission.
func Fit(d Data, max int) (Data, bool) {
	if d.Size() <= max {
		return d, false
	}
	payload, _ := json.Marshal(struct {
		Kind  string `json:"original_kind"`
		Bytes int    `json:"original_bytes"`
	}{d.Kind, d.Size()})
	return Data{Kind: "omitted", Agent: d.Agent, Time: d.Time, Payload: payload}, true
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
