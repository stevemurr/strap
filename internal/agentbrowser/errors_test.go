package agentbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadProtocolAndContentFailures(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"version", "version failed"}, {"invalid envelope", "invalid JSON"},
		{"rejected", "site failed"}, {"bad data", "data:"},
		{"readiness error", "did not become readable"},
		{"missing content", "rendered page content"}, {"missing URL", "rendered page content"},
		{"unrendered", "rendered page content"}, {"PDF", "unsupported page type"},
		{"empty", "no readable content"}, {"blocked", "challenge page"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			closed := 0
			c := &Client{ExecutablePath: "/host/browser", MaxChars: 1000, MaxBytes: 100000}
			c.run = func(_ context.Context, _ string, args []string, _ string, _ int, _ ...string) ([]byte, error) {
				if args[0] == "--version" {
					if tc.mode == "version" {
						return nil, errors.New("version failed")
					}
					return []byte("agent-browser " + Version), nil
				}
				if i := slices.Index(args, "--executable-path"); i < 0 || args[i+1] != "/host/browser" {
					t.Error("executable override lost", args)
				}
				command := args[slices.Index(args, "--executable-path")+2:]
				switch command[0] {
				case "close":
					closed++
					return response(nil), nil
				case "open":
					switch tc.mode {
					case "invalid envelope":
						return []byte(`{"data":{}}`), nil
					case "rejected":
						return []byte(`{"success":false,"error":"site failed"}`), nil
					}
				case "eval":
					if command[1] == readyScript {
						if tc.mode == "readiness error" {
							return nil, errors.New("eval failed")
						}
						if tc.mode == "bad data" {
							return response("wrong shape"), nil
						}
						return response(map[string]bool{"result": true}), nil
					}
					return response(map[string]any{"result": map[string]any{"url": "https://example.com", "blocked": tc.mode == "blocked"}}), nil
				case "read":
					data := map[string]any{"content": "body", "contentType": "text/html", "finalUrl": "https://example.com", "source": "active-tab-html"}
					switch tc.mode {
					case "missing content":
						delete(data, "content")
					case "missing URL":
						delete(data, "finalUrl")
					case "unrendered":
						data["source"] = "http-fetch"
					case "PDF":
						data["contentType"] = "application/pdf"
					case "empty":
						data["content"] = " \n\t "
					}
					return response(data), nil
				}
				return response(nil), nil
			}
			_, err := c.Read(context.Background(), "https://example.com")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			wantClosed := 1
			if tc.mode == "version" {
				wantClosed = 0
			}
			if closed != wantClosed {
				t.Fatalf("closed %d times; want %d", closed, wantClosed)
			}
		})
	}
}

func TestReadDefaultRunnerReportsMissingProgram(t *testing.T) {
	c := &Client{Program: filepath.Join(t.TempDir(), "missing")}
	if _, err := c.Read(context.Background(), "https://example.com"); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatal(err)
	}
}

func TestReadCleanupIncludesPIDInspectionFailure(t *testing.T) {
	c := &Client{}
	c.run = func(_ context.Context, _ string, args []string, dir string, _ int, _ ...string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("agent-browser " + Version), nil
		}
		if args[slices.Index(args, "--json")+1] == "close" {
			id := args[slices.Index(args, "--namespace")+1]
			pidDir := filepath.Join(dir, "namespaces", id, "run")
			if err := os.MkdirAll(pidDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pidDir, "page.pid"), []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			return nil, errors.New("close failed")
		}
		return nil, errors.New("open failed")
	}
	_, err := c.Read(context.Background(), "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "open failed") || !strings.Contains(err.Error(), "close failed") {
		t.Fatal(err)
	}
}

func TestReadTemporaryConfigurationFailures(t *testing.T) {
	for _, stage := range []string{"directory", "config"} {
		t.Run(stage, func(t *testing.T) {
			want := errors.New(stage + " failed")
			c := &Client{run: func(_ context.Context, _ string, args []string, _ string, _ int, _ ...string) ([]byte, error) {
				if !slices.Equal(args, []string{"--version"}) {
					t.Fatal("launched without isolated config", args)
				}
				return []byte("agent-browser " + Version), nil
			}}
			var dir string
			c.mkdirTemp = func(base, pattern string) (string, error) {
				if stage == "directory" {
					return "", want
				}
				var err error
				dir, err = os.MkdirTemp(base, pattern)
				return dir, err
			}
			c.writeFile = func(string, []byte, os.FileMode) error { return want }
			if _, err := c.Read(context.Background(), "https://example.com"); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if dir != "" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("temporary directory leaked", err)
				}
			}
		})
	}
}

func TestReadCleanupFallbackPreservesNavigationError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		killed    bool
		stopErr   error
		wantClose bool
	}{
		{"stopped", true, nil, false}, {"not found", false, nil, true}, {"stop failed", false, errors.New("signal failed"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			navigationErr := errors.New("navigation failed")
			closeErr := errors.New("close failed")
			c := &Client{}
			var directory, namespace string
			c.run = func(ctx context.Context, _ string, args []string, dir string, _ int, _ ...string) ([]byte, error) {
				if args[0] == "--version" {
					return []byte("agent-browser " + Version), nil
				}
				directory, namespace = dir, args[slices.Index(args, "--namespace")+1]
				if args[slices.Index(args, "--json")+1] == "close" {
					return nil, closeErr
				}
				return nil, navigationErr
			}
			calls := 0
			c.interrupt = func(ctx context.Context, dir, id string) (bool, error) {
				calls++
				if ctx.Err() != nil || dir != directory || id != namespace {
					t.Error("wrong cleanup context/session")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("unbounded cleanup")
				}
				return tc.killed, tc.stopErr
			}
			_, err := c.Read(context.Background(), "https://example.com")
			if calls != 1 || !errors.Is(err, navigationErr) || errors.Is(err, closeErr) != tc.wantClose || (tc.stopErr != nil && !errors.Is(err, tc.stopErr)) {
				t.Fatal(calls, err)
			}
		})
	}
}
