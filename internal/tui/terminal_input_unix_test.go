//go:build darwin || linux

package tui

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
)

func TestFramedInputPreservesCancellation(t *testing.T) {
	file, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer writer.Close()
	input := newFramedTerminalInput(file)
	if input.Fd() != file.Fd() || input.Name() != file.Name() {
		t.Fatal("lost terminal identity")
	}
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, err = writer.Write([]byte("\x1b[<35;"))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	if n, err := reader.Read(buf); n != 0 || err != nil {
		t.Fatalf("partial sequence: %q %v", buf[:n], err)
	}
	done := make(chan error, 1)
	go func() { _, err := reader.Read(buf); done <- err }()
	if !reader.Cancel() {
		t.Fatal("input no longer supports cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, cancelreader.ErrCanceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial sequence blocked terminal release")
	}
	// Restore with the same input wrapper, as Bubble Tea does after suspension.
	restored, err := cancelreader.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := writer.Write([]byte("8;12M")); err != nil {
		t.Fatal(err)
	}
	if n, err := restored.Read(buf); err != nil || string(buf[:n]) != "\x1b[<35;8;12M" {
		t.Fatalf("restored: %q %v", buf[:n], err)
	}
}

func TestFramedInputEscapeTimeout(t *testing.T) {
	file, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer writer.Close()
	input := newFramedTerminalInput(file)
	if _, err := writer.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { p := make([]byte, 256); n, _ := input.Read(p); done <- string(p[:n]) }()
	select {
	case got := <-done:
		if got != "\x1b" {
			t.Fatalf("Escape: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Escape waits indefinitely for another key")
	}
}
