package eventlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONLExclusiveCreationAndInterruptedInspection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	s, err := NewJSONL(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewJSONL(path, "other"); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if _, err = s.Append(ctx, Data{Kind: "test", Payload: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"partial":`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err := OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(ctx)
	p, err := r.Read(ctx, Query{Limit: 1})
	if err != nil || p.Latest != 1 || p.Sealed || len(p.Events) != 1 {
		t.Fatal(p, err)
	}
	if _, err = r.Append(ctx, Data{Kind: "test", Payload: []byte(`{}`)}); !errors.Is(err, ErrReadOnly) {
		t.Fatal(err)
	}
}
func TestJSONLSealedReopenAndCorruptTail(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	s, err := NewJSONL(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Seal(ctx, Outcome{Omitted: 3}); err != nil {
		t.Fatal(err)
	}
	s.Close(ctx)
	r, err := OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Read(ctx, Query{Limit: 1})
	if err != nil || !p.Sealed || p.Outcome.Omitted != 3 {
		t.Fatal(p, err)
	}
	r.Close(ctx)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("broken\n")
	f.Close()
	if r, err = OpenJSONL(ctx, path); err == nil {
		r.Close(ctx)
		t.Fatal("corrupt trace accepted")
	}
}
func TestJSONLWriteAndSyncFailuresAreNotRetried(t *testing.T) {
	ctx := context.Background()
	for _, failWrite := range []bool{true, false} {
		s, err := NewJSONL(filepath.Join(t.TempDir(), "trace.jsonl"), "test")
		if err != nil {
			t.Fatal(err)
		}
		syncs := 0
		s.syncFile = func() error { syncs++; return errors.New("sync failed") }
		if failWrite {
			s.file.Close()
		}
		if err = s.Seal(ctx, Outcome{}); err == nil {
			t.Fatal("failure hidden")
		}
		if err = s.Seal(ctx, Outcome{}); err == nil {
			t.Fatal("failure retried")
		}
		if syncs > 1 || s.outcome != nil {
			t.Fatal(syncs, s.outcome)
		}
		if _, err = s.Append(ctx, Data{Kind: "test", Payload: []byte(`{}`)}); err == nil {
			t.Fatal("append after ambiguous failure")
		}
		s.Close(ctx)
	}
}

func TestFailedSealDoesNotExposeTerminalRecord(t *testing.T) {
	s, err := NewJSONL(filepath.Join(t.TempDir(), "trace.jsonl"), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if _, err = s.Append(context.Background(), Data{Kind: "test", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	s.syncFile = func() error { return errors.New("disk sync failed") }
	if err = s.Seal(context.Background(), Outcome{}); err == nil {
		t.Fatal("seal succeeded")
	}
	p, err := s.Read(context.Background(), Query{Limit: 10})
	if err != nil || p.Latest != 1 || len(p.Events) != 1 || p.Sealed {
		t.Fatal(p, err)
	}
}
