// Package agentbrowser reads rendered pages through isolated agent-browser
// sessions. It never attaches to the user's browser or restores login state.
package agentbrowser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/stevemurr/strap/internal/webprocess"
)

type Link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type Page struct {
	URL            string
	Title          string
	Content        string
	ContentType    string
	Truncated      bool
	Links          []Link
	LinksTruncated bool
}

type Client struct {
	Program        string
	ExecutablePath string
	MaxChars       int
	MaxBytes       int
	run            func(context.Context, string, []string, string, int, ...string) ([]byte, error)
	mkdirTemp      func(string, string) (string, error)
	writeFile      func(string, []byte, os.FileMode) error
	interrupt      func(context.Context, string, string) (bool, error)
}

type envelope struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

// These are host-authored DOM reads, never model- or page-supplied scripts.
const readyScript = `(async () => {
 const deadline = Date.now() + 2000;
 do {
   const text = (document.body?.innerText || '').trim();
   if (text.length > 0 && !/^(loading|please wait)[.!…\s]*$/i.test(text)) return true;
   await new Promise(resolve => setTimeout(resolve, 50));
 } while (Date.now() < deadline);
 return false;
})()`
const metadataScript = `(() => {
 const links = []; const seen = new Set(); let skipped = false;
 for (const a of document.querySelectorAll('a[href]')) {
   if (!/^https?:$/.test(a.protocol) || a.username || a.password || seen.has(a.href)) continue;
   seen.add(a.href);
   if (a.href.length > 8192) { skipped = true; continue; }
   if (links.length < 200) links.push({text: (a.innerText || a.textContent || '').trim().slice(0, 500), url: a.href});
 }
 const shortGate = (document.body?.innerText || '').length < 4000 && /^(just a moment|attention required|access denied)[.!…\s]*$/i.test(document.title.trim());
 const blocked = shortGate || !!document.querySelector('#challenge-running, #challenge-stage, form#challenge-form, .anomaly-modal');
 return {url: location.href, title: document.title, links, links_truncated: skipped || seen.size > 200, blocked};
})()`

func (c *Client) Read(ctx context.Context, url string) (page Page, err error) {
	run := c.run
	if run == nil {
		run = webprocess.Run
	}
	// Darwin's per-user TMPDIR is too long for the daemon's Unix socket path.
	tempBase := ""
	if runtime.GOOS == "darwin" {
		tempBase = "/tmp"
	}
	mkdirTemp, writeFile, interrupt := c.mkdirTemp, c.writeFile, c.interrupt
	if mkdirTemp == nil {
		mkdirTemp = os.MkdirTemp
	}
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if interrupt == nil {
		interrupt = interruptBrowser
	}
	dir, err := mkdirTemp(tempBase, "strap-browser-")
	if err != nil {
		return page, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	config := filepath.Join(dir, "config.json")
	if err := writeFile(config, []byte(`{}`), 0600); err != nil {
		return page, err
	}
	var nonce [16]byte
	// crypto/rand.Read always fills the buffer and never returns an error.
	rand.Read(nonce[:])
	id := "strap-" + hex.EncodeToString(nonce[:])
	base := []string{"--config", config, "--namespace", id, "--session", "page", "--profile", filepath.Join(dir, "profile"), "--idle-timeout", "30s", "--json"}
	if c.ExecutablePath != "" {
		base = append(base, "--executable-path", c.ExecutablePath)
	}
	invoke := func(ctx context.Context, output any, command ...string) error {
		args := append(append([]string(nil), base...), command...)
		// Keep daemon configuration stable across commands. Changing the default
		// timeout between open and extraction can restart the background session.
		out, err := run(ctx, c.Program, args, dir, c.MaxBytes, "AGENT_BROWSER_DEFAULT_TIMEOUT=25000", "AGENT_BROWSER_SOCKET_DIR="+dir)
		if err != nil {
			return fmt.Errorf("agent-browser %s: %w", command[0], err)
		}
		var response envelope
		if err := json.Unmarshal(out, &response); err != nil || response.Success == nil {
			return fmt.Errorf("agent-browser %s returned invalid JSON", command[0])
		}
		if !*response.Success {
			return fmt.Errorf("agent-browser %s: %s", command[0], response.Error)
		}
		if output != nil {
			if err := json.Unmarshal(response.Data, output); err != nil {
				return fmt.Errorf("agent-browser %s data: %w", command[0], err)
			}
		}
		return nil
	}
	// A cancelled CLI does not own the detached daemon's lifetime. Always close
	// our named session using a fresh budget, and surface cleanup failures.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		first, stop := context.WithTimeout(cleanup, 2*time.Second)
		closeErr := invoke(first, nil, "close")
		stop()
		if closeErr != nil {
			// If close is serialized behind navigation, the fallback
			// terminates the owned session; its profile and sockets are all in dir.
			killed, stopErr := interrupt(cleanup, dir, id)
			if killed && stopErr == nil {
				closeErr = nil
			}
			closeErr = errors.Join(closeErr, stopErr)
		}
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close browser session %s: %w", id, closeErr))
		}
	}()
	if err := invoke(ctx, nil, "open", url); err != nil {
		return page, err
	}
	ready, cancel := context.WithTimeout(ctx, 3*time.Second)
	var readable struct {
		Result bool `json:"result"`
	}
	err = invoke(ready, &readable, "eval", readyScript)
	cancel()
	if err != nil {
		return page, fmt.Errorf("page did not become readable: %w", err)
	}
	if !readable.Result {
		return page, errors.New("page remained empty or loading after the readiness budget")
	}
	var read struct {
		Content     *string `json:"content"`
		ContentType string  `json:"contentType"`
		FinalURL    string  `json:"finalUrl"`
		Source      string  `json:"source"`
		Truncated   bool    `json:"truncated"`
	}
	if err := invoke(ctx, &read, "read", "--max-output", strconv.Itoa(c.MaxChars)); err != nil {
		return page, err
	}
	if read.Content == nil || read.FinalURL == "" || !strings.HasPrefix(read.Source, "active-tab") {
		return page, errors.New("agent-browser did not return rendered page content")
	}
	if !strings.HasPrefix(read.ContentType, "text/") {
		return page, fmt.Errorf("unsupported page type %q; open_url reads HTML and text", read.ContentType)
	}
	if strings.TrimSpace(*read.Content) == "" {
		return page, errors.New("page returned no readable content")
	}
	var metadata struct {
		Result struct {
			URL            string `json:"url"`
			Title          string `json:"title"`
			Links          []Link `json:"links"`
			LinksTruncated bool   `json:"links_truncated"`
			Blocked        bool   `json:"blocked"`
		} `json:"result"`
	}
	if err := invoke(ctx, &metadata, "eval", metadataScript); err != nil {
		return page, err
	}
	if metadata.Result.URL != read.FinalURL {
		return page, errors.New("page navigated during extraction; open the URL again")
	}
	if metadata.Result.Blocked {
		return page, errors.New("site returned an access or browser challenge page instead of readable source content")
	}
	return Page{URL: read.FinalURL, Title: metadata.Result.Title, Content: *read.Content, ContentType: read.ContentType, Truncated: read.Truncated, Links: metadata.Result.Links, LinksTruncated: metadata.Result.LinksTruncated}, nil
}
