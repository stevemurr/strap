package eventlog

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const maxJSONLRecord = MaxRecordBytes

var ErrReadOnly = errors.New("event store is read only")

// JSONL keeps accepted history on disk. Its temporary disk index is rebuildable
// and permits bounded sequential pages without rescanning earlier records.
type JSONL struct {
	*storeState
	writeMu     sync.Mutex
	file, index *os.File
	end         int64
	readonly    bool
	syncFile    func() error
	active      int
	drained     chan struct{}
	closeOnce   sync.Once
	closeErr    error
}

func newJSONL(f *os.File, session string) (*JSONL, error) {
	index, err := os.CreateTemp("", "strap-log-index-*")
	if err != nil {
		return nil, err
	}
	return &JSONL{storeState: newState(session), file: f, index: index, syncFile: f.Sync, drained: make(chan struct{})}, nil
}
func NewJSONL(path, session string) (*JSONL, error) {
	if session == "" {
		return nil, errors.New("session identity is required")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	s, err := newJSONL(f, session)
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return s, nil
}

// OpenJSONL is finite archive inspection, not execution recovery.
func OpenJSONL(ctx context.Context, path string) (*JSONL, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s, err := newJSONL(f, "")
	if err != nil {
		f.Close()
		return nil, err
	}
	s.readonly = true
	ok := false
	defer func() {
		if !ok {
			_ = s.Close(context.Background())
		}
	}()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := readLine(r)
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
		if e.Schema != SchemaVersion || e.Sequence != s.latest+1 || e.Session == "" || s.session != "" && e.Session != s.session || s.outcome != nil {
			return nil, errors.New("invalid trace schema, sequence, identity, or terminal order")
		}
		if err = e.Data.Validate(); err != nil {
			return nil, err
		}
		if err = s.writeIndex(e.Sequence, s.end); err != nil {
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
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() != s.end {
		s.outcome = nil
	}
	if s.outcome == nil {
		s.failed = errors.New("interrupted archive: no confirmed terminal record")
	}
	ok = true
	return s, nil
}
func (s *JSONL) beginIO() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return ErrDisposed
	}
	s.active++
	return nil
}
func (s *JSONL) endIO() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.disposed && s.active == 0 {
		close(s.drained)
	}
}
func (s *JSONL) writeIndex(seq uint64, offset int64) error {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(offset))
	n, err := s.index.WriteAt(b[:], int64(seq-1)*8)
	if err == nil && n != 8 {
		err = io.ErrShortWrite
	}
	return err
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
	if err := d.Validate(); err != nil {
		return Event{}, err
	}
	if d.Size() > MaxRecordBytes {
		return Event{}, ErrRecordSize
	}
	return s.write(ctx, d, nil)
}
func (s *JSONL) write(ctx context.Context, d Data, outcome *Outcome) (Event, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if err := s.beginIO(); err != nil {
		return Event{}, err
	}
	defer s.endIO()
	s.mu.Lock()
	if s.readonly {
		s.mu.Unlock()
		return Event{}, ErrReadOnly
	}
	if outcome != nil && s.outcome != nil {
		if *outcome != *s.outcome {
			s.mu.Unlock()
			return Event{}, errors.New("conflicting shutdown outcome")
		}
		s.mu.Unlock()
		return Event{}, nil
	}
	if err := s.writable(); err != nil {
		s.mu.Unlock()
		return Event{}, err
	}
	seq, offset := s.latest+1, s.end
	s.mu.Unlock()
	e := Event{Schema: SchemaVersion, Session: s.session, Sequence: seq, Data: d.Clone()}
	raw, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxJSONLRecord {
		return Event{}, ErrRecordSize
	}
	n, err := s.file.WriteAt(raw, offset)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.writeIndex(seq, offset)
	}
	if err == nil && outcome != nil {
		err = s.syncFile()
	}
	if err != nil {
		s.Fail(err)
		return Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Event{}, err
	}
	if err := ctx.Err(); err != nil {
		s.failed = errors.Join(ErrCapture, err)
		s.signal()
		return Event{}, s.failed
	}
	s.latest = seq
	s.end = offset + int64(n)
	if outcome != nil {
		o := *outcome
		s.outcome = &o
	}
	s.signal()
	return e, nil
}
func (s *JSONL) Read(ctx context.Context, q Query) (Page, error) {
	if err := q.Validate(); err != nil {
		return Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err := s.beginIO(); err != nil {
		return Page{}, err
	}
	defer s.endIO()
	s.mu.Lock()
	end := s.end
	p := Page{Latest: s.latest, Next: q.After, Sealed: s.outcome != nil, Head: s.head()}
	if s.latest > 0 {
		p.Earliest = 1
	}
	if s.outcome != nil {
		o := *s.outcome
		p.Outcome = &o
	}
	s.mu.Unlock()
	if err := cursor(q, p.Earliest, p.Latest); err != nil {
		return p, err
	}
	if q.After == p.Latest {
		return p, nil
	}
	var offset [8]byte
	if _, err := s.index.ReadAt(offset[:], int64(q.After)*8); err != nil {
		return p, err
	}
	start := int64(binary.LittleEndian.Uint64(offset[:]))
	r := bufio.NewReaderSize(io.NewSectionReader(s.file, start, end-start), 64<<10)
	bytes := 0
	for len(p.Events) < q.Limit {
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
		if e.Sequence != p.Next+1 || e.Session != s.session {
			return p, errors.New("corrupt event sequence")
		}
		if q.MaxBytes > 0 && bytes+e.Size() > q.MaxBytes {
			if len(p.Events) == 0 {
				return p, ErrPageSize
			}
			break
		}
		p.Events = append(p.Events, e)
		p.Next = e.Sequence
		bytes += e.Size()
	}
	return p, nil
}
func (s *JSONL) Seal(ctx context.Context, o Outcome) error {
	_, err := s.write(ctx, terminal(o), &o)
	return err
}
func (s *JSONL) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.disposed {
		s.disposed = true
		s.signal()
		if s.active == 0 {
			close(s.drained)
		}
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.drained:
	}
	s.closeOnce.Do(func() { s.closeErr = errors.Join(s.file.Close(), s.index.Close(), os.Remove(s.index.Name())) })
	return s.closeErr
}
