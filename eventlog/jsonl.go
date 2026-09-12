package eventlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const maxJSONLRecord = 16 << 20

var ErrReadOnly = errors.New("event store is read only")

// JSONL retains all committed events on disk. Reads scan the file with bounded
// buffers instead of retaining a growing in-memory event/offset index. Successful
// Seal syncs the file; Append and Log.Flush alone do not promise durability.
type JSONL struct {
	mu                 sync.Mutex
	file               *os.File
	session            string
	end                int64
	latest             uint64
	outcome            *Outcome
	readonly, disposed bool
	failed             error
	syncFile           func() error
}

// NewJSONL creates an exclusive, private file. Existing traces are never overwritten.
func NewJSONL(path, session string) (*JSONL, error) {
	if session == "" {
		return nil, errors.New("session identity is required")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return &JSONL{file: f, session: session, syncFile: f.Sync}, nil
}

// OpenJSONL opens a completed or interrupted trace for inspection only. A partial
// final line is ignored; a malformed complete record or sequence gap is an error.
// A file without a complete terminal record remains unsealed.
func OpenJSONL(ctx context.Context, path string) (*JSONL, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s := &JSONL{file: f, readonly: true}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := readLine(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var e Event
		if err = json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("invalid event record: %w", err)
		}
		if e.Schema != SchemaVersion || e.Sequence != s.latest+1 || e.Session == "" || (s.session != "" && e.Session != s.session) || s.outcome != nil {
			return nil, errors.New("invalid trace schema, sequence, identity, or terminal order")
		}
		if err = e.Data.Validate(); err != nil {
			return nil, err
		}
		s.session = e.Session
		s.latest = e.Sequence
		s.end += int64(len(line))
		if e.Kind == "session_closed" {
			var o Outcome
			if err = json.Unmarshal(e.Payload, &o); err != nil {
				return nil, err
			}
			s.outcome = &o
		}
	}
	// A partial tail after a terminal record invalidates finalization as well.
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() != s.end {
		s.outcome = nil
	}
	ok = true
	return s, nil
}
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxJSONLRecord {
			return nil, errors.New("event record exceeds JSONL read limit")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
func (s *JSONL) Append(ctx context.Context, d Data) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if err := d.Validate(); err != nil {
		return Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Event{}, err
	}
	if s.outcome != nil {
		return Event{}, ErrSealed
	}
	return s.append(d)
}
func (s *JSONL) writable() error {
	if s.disposed {
		return ErrDisposed
	}
	if s.readonly {
		return ErrReadOnly
	}
	return s.failed
}
func (s *JSONL) append(d Data) (Event, error) {
	d, _ = Fit(d, maxJSONLRecord/2) // Leave room for envelope and JSON escaping.
	e := Event{Schema: SchemaVersion, Session: s.session, Sequence: s.latest + 1, Data: d.Clone()}
	raw, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxJSONLRecord {
		return Event{}, errors.New("encoded event exceeds JSONL record limit")
	}
	n, err := s.file.WriteAt(raw, s.end)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.failed = err
		return Event{}, err
	}
	s.latest = e.Sequence
	s.end += int64(n)
	return e, nil
}
func (s *JSONL) Read(ctx context.Context, q Query) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err := q.Validate(); err != nil {
		return Page{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return Page{}, ErrDisposed
	}
	p := Page{Latest: s.latest, Next: q.After, Sealed: s.outcome != nil}
	if s.latest > 0 {
		p.Earliest = 1
	}
	if s.outcome != nil {
		o := *s.outcome
		p.Outcome = &o
	}
	if err := cursor(q, p.Earliest, p.Latest); err != nil {
		return p, err
	}
	if q.After == s.latest {
		return p, nil
	}
	r := bufio.NewReaderSize(io.NewSectionReader(s.file, 0, s.end), 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		line, err := readLine(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return p, err
		}
		var e Event
		if err = json.Unmarshal(line, &e); err != nil {
			return p, err
		}
		if e.Sequence > q.After {
			p.Events = append(p.Events, e)
			p.Next = e.Sequence
			if len(p.Events) >= q.Limit {
				break
			}
		}
	}
	return p, nil
}
func (s *JSONL) Seal(ctx context.Context, o Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return err
	}
	if s.outcome != nil {
		if *s.outcome != o {
			return errors.New("conflicting shutdown outcome")
		}
		return nil
	}
	d := terminal(o)
	if d.Size() > maxJSONLRecord/2 {
		return errors.New("shutdown outcome exceeds JSONL record limit")
	}
	if _, err := s.append(d); err != nil {
		return err
	}
	if err := s.syncFile(); err != nil {
		s.failed = err
		return err
	}
	s.outcome = &o
	return nil
}
func (s *JSONL) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return nil
	}
	// os.File.Close releases its handle even when it reports an I/O error.
	err := s.file.Close()
	s.disposed = true
	return err
}
