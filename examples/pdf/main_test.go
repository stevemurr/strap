package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/stevemurr/strap/tool"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func pdfServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	var calls atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "failure" {
			http.Error(w, "model unavailable", 503)
			return
		}
		if mode == "stall" {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if mode == "images" && calls.Add(1) == 1 {
			path, err := filepath.Abs("../../tool/testdata/pages.pdf")
			if err != nil {
				t.Error(err)
			}
			args, _ := tool.MarshalInput(map[string]any{"path": path, "pages": []int{1}})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "pdf", "type": "function", "function": map[string]any{"name": "read_pdf", "arguments": string(args)}}}}}}})
			return
		}
		if mode == "images" {
			data, _ := json.Marshal(request)
			if !strings.Contains(string(data), "data:image/png;base64,") {
				t.Error("model did not receive page image")
			}
		}
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Page 1 transcribed."}}]}`)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server
}

// read_pdf rasterizes through Poppler, so without it the example cannot
// produce the page images it exists to demonstrate. That is a missing tool on
// the machine, not a defect in the example.
func requirePoppler(t *testing.T) {
	t.Helper()
	for _, program := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("%s is not installed; install Poppler to run this example", program)
		}
	}
}

func TestPDFExampleReadsImagesThroughHTTP(t *testing.T) {
	requirePoppler(t)
	server := pdfServer(t, "images")
	oldFlags, oldArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine = oldFlags; os.Args = oldArgs }()
	flag.CommandLine = flag.NewFlagSet("pdf", flag.ContinueOnError)
	os.Args = []string{"pdf", "-base-url", server.URL, "-model", "test", "-timeout", "3s"}
	main()
}
func TestPDFExampleErrors(t *testing.T) {
	if err := run(context.Background(), "missing", "invalid", "model"); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, "missing", "http://127.0.0.1:1", "model"); err == nil {
		t.Fatal("canceled run succeeded")
	}
	for _, mode := range []string{"no images", "failure", "stall"} {
		t.Run(mode, func(t *testing.T) {
			server := pdfServer(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			err := run(ctx, "missing", server.URL, "model")
			if err == nil {
				t.Fatal("expected error")
			}
			if mode == "no images" && !strings.Contains(err.Error(), "without receiving PDF") {
				t.Fatal(err)
			}
		})
	}
}

func TestPDFExampleMainFailureExit(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine = oldFlags; os.Args = oldArgs }()
	flag.CommandLine = flag.NewFlagSet("pdf", flag.ContinueOnError)
	os.Args = []string{"pdf", "-base-url", "invalid"}
	code := 0
	mainWithExit(func(got int) { code = got })
	if code != 1 {
		t.Fatal(code)
	}
}
