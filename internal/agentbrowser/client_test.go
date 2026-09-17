package agentbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func response(data any) []byte {
	out, _ := json.Marshal(map[string]any{"success": true, "data": data, "error": nil})
	return out
}

func TestReadUsesIsolatedSessionsAndRenderedDOM(t *testing.T) {
	var mu sync.Mutex
	urls := map[string]string{}
	operations := map[string][]string{}
	directories := map[string]string{}
	c := &Client{Program: "fake", MaxChars: 1000, MaxBytes: 100000}
	c.run = func(ctx context.Context, program string, args []string, dir string, limit int, env ...string) ([]byte, error) {
		if slices.Equal(args, []string{"--version"}) {
			t.Error("page reads must not probe or gate on browser version")
			return nil, errors.New("unexpected version probe")
		}
		mu.Lock()
		defer mu.Unlock()
		id := args[slices.Index(args, "--namespace")+1]
		config, err := os.ReadFile(args[slices.Index(args, "--config")+1])
		if err != nil || string(config) != "{}" {
			t.Error("missing explicit isolated config", err)
		}
		if args[slices.Index(args, "--session")+1] != "page" || !strings.HasPrefix(id, "strap-") || !slices.Equal(env, []string{"AGENT_BROWSER_DEFAULT_TIMEOUT=25000", "AGENT_BROWSER_SOCKET_DIR=" + dir}) {
			t.Errorf("invalid session args %v", args)
		}
		command := args[slices.Index(args, "--json")+1:]
		operations[id] = append(operations[id], command[0])
		directories[id] = dir
		switch command[0] {
		case "open":
			urls[id] = command[1]
			return response(map[string]any{"url": command[1]}), nil
		case "read":
			if !slices.Equal(command, []string{"read", "--max-output", "1000"}) {
				t.Error("read must not contain a URL", command)
			}
			return response(map[string]any{"content": "# Heading\n\n```go\nfunc main() {}\n```", "contentType": "text/html", "finalUrl": urls[id], "source": "active-tab-html", "truncated": false}), nil
		case "eval":
			if command[1] == readyScript {
				return response(map[string]bool{"result": true}), nil
			}
			if command[1] != metadataScript {
				t.Error("unexpected script")
			}
			return response(map[string]any{"result": map[string]any{"url": urls[id], "title": "Page", "links": []Link{{Text: "Next", URL: urls[id] + "/next"}}}}), nil
		case "close":
			if ctx.Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			return response(map[string]any{"closed": true}), nil
		}
		return nil, errors.New("unknown command")
	}
	var wg sync.WaitGroup
	for _, url := range []string{"https://a.test/?q=$(literal)", "https://b.test/"} {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			p, err := c.Read(context.Background(), url)
			if err != nil || p.URL != url || len(p.Links) != 1 || p.Links[0].URL != url+"/next" || !strings.Contains(p.Content, "```go") {
				t.Errorf("%+v %v", p, err)
			}
		}(url)
	}
	wg.Wait()
	if len(operations) != 2 {
		t.Fatal("browser session shared across calls")
	}
	for id, ops := range operations {
		if !slices.Equal(ops, []string{"open", "eval", "read", "eval", "close"}) {
			t.Fatal(ops)
		}
		if _, err := os.Stat(directories[id]); !os.IsNotExist(err) {
			t.Fatal("temporary config directory leaked")
		}
	}
}

func TestReadFailuresAlwaysCloseSessionWithFreshContext(t *testing.T) {
	for _, mode := range []string{"open", "ready", "read", "eval", "cancel", "malformed", "navigation", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			closed := 0
			c := &Client{Program: "fake", MaxChars: 1000, MaxBytes: 100000}
			c.run = func(ctx context.Context, _ string, args []string, _ string, _ int, _ ...string) ([]byte, error) {
				if args[0] == "--version" {
					t.Error("page reads must not probe or gate on browser version")
					return nil, errors.New("unexpected version probe")
				}
				cmd := args[slices.Index(args, "--json")+1]
				if cmd == "eval" && args[len(args)-1] == readyScript {
					if mode == "ready" {
						return response(map[string]bool{"result": false}), nil
					}
					return response(map[string]bool{"result": true}), nil
				}
				if cmd == "close" {
					closed++
					if ctx.Err() != nil {
						t.Error("cleanup context cancelled")
					}
					if mode == "cleanup" {
						return nil, errors.New("cleanup failed")
					}
					return response(nil), nil
				}
				if cmd == mode {
					return nil, errors.New("phase failed")
				}
				if cmd == "read" {
					if mode == "cancel" {
						cancel()
						return nil, ctx.Err()
					}
					if mode == "malformed" {
						return []byte(`{broken`), nil
					}
					return response(map[string]any{"content": "body", "contentType": "text/html", "finalUrl": "https://example.com", "source": "active-tab-html"}), nil
				}
				if cmd == "eval" {
					url := "https://example.com"
					if mode == "navigation" {
						url = "https://changed.example"
					}
					return response(map[string]any{"result": map[string]string{"url": url}}), nil
				}
				return response(nil), nil
			}
			_, err := c.Read(ctx, "https://example.com")
			if err == nil || closed != 1 {
				t.Fatalf("err=%v closed=%d", err, closed)
			}
		})
	}
}
