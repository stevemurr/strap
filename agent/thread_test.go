package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func TestThreadSnapshotsOwnNestedData(t *testing.T) {
	original := provider.Message{Role: "assistant", Content: content.Content{{Text: "checking"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2}}}}, Envelope: &message.Message{Content: "assignment"}, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)}}}
	var thread thread
	thread.append(original)
	original.Content[0].Text = "changed"
	original.Content[1].Image.Data[0] = 9
	original.Envelope.Content = "changed"
	original.ToolCalls[0].Arguments[0] = '!'
	want := thread.requestMessages()
	page, err := thread.snapshot(TranscriptQuery{})
	if err != nil {
		t.Fatal(err)
	}
	page.Entries[0].Message.Content[0].Text = "changed again"
	page.Entries[0].Message.Content[1].Image.Data[0] = 8
	page.Entries[0].Message.Envelope.Content = "again"
	page.Entries[0].Message.ToolCalls[0].Arguments[0] = '!'
	if got := thread.requestMessages(); !reflect.DeepEqual(got, want) || got[0].Content[0].Text != "checking" || got[0].Content[1].Image.Data[0] != 1 {
		t.Fatalf("snapshot aliases thread: %+v", got)
	}
	want[0].Content[0].Text = "provider mutation"
	if got := thread.requestMessages(); got[0].Content[0].Text != "checking" {
		t.Fatal("provider snapshot aliases thread")
	}
}

func TestThreadPagingIsStableAcrossAppends(t *testing.T) {
	var thread thread
	for i := 1; i <= 45; i++ {
		thread.append(provider.Message{Role: "user", Content: content.Text(fmt.Sprint(i))})
	}
	latest, err := thread.snapshot(TranscriptQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Entries) != 20 || latest.Entries[0].Position != 26 || !latest.HasEarlier {
		t.Fatal(latest)
	}
	thread.append(provider.Message{Role: "assistant", Content: content.Text("new")})
	earlier, err := thread.snapshot(TranscriptQuery{Before: latest.Entries[0].Position})
	if err != nil {
		t.Fatal(err)
	}
	if earlier.Entries[0].Position != 6 || earlier.Entries[19].Position != 25 {
		t.Fatal(earlier)
	}
	first, err := thread.snapshot(TranscriptQuery{Before: earlier.Entries[0].Position})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 5 || first.HasEarlier {
		t.Fatal(first)
	}
	empty, err := thread.snapshot(TranscriptQuery{Before: 1})
	if err != nil || len(empty.Entries) != 0 || empty.HasEarlier {
		t.Fatalf("%+v %v", empty, err)
	}
	for _, q := range []TranscriptQuery{{Limit: -1}, {Limit: 101}, {Before: ^uint64(0)}} {
		if _, err := thread.snapshot(q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
}

func TestThreadConcurrentInspection(t *testing.T) {
	var thread thread
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			thread.append(provider.Message{Role: "assistant", Content: content.Text(fmt.Sprint(i))})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			page, err := thread.snapshot(TranscriptQuery{})
			if err != nil {
				t.Error(err)
				return
			}
			for j := 1; j < len(page.Entries); j++ {
				if page.Entries[j].Position != page.Entries[j-1].Position+1 {
					t.Error("noncontiguous snapshot")
				}
			}
			thread.requestMessages()
		}
	}()
	wg.Wait()
	if len(thread.requestMessages()) != 200 {
		t.Fatal("lost writes")
	}
}
