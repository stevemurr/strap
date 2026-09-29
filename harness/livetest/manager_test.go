package livetest_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/machine"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/modelcatalog"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

const readme = `# textkit

Implement these functions in text.go (package textkit). Keep the package
name, file name and signatures.

1. Slugify(s string) string lowercases s and turns every run of characters
   that are not ASCII letters or digits into a single "-", with no leading or
   trailing "-". Slugify("Hello, World!") == "hello-world" and
   Slugify("  Go  is fun ") == "go-is-fun".
2. Truncate(s string, n int) string returns s unchanged when it has at most n
   runes, and otherwise its first n-1 runes followed by "…".
   Truncate("abcdef", 4) == "abc…". When n < 1 it returns "".
`

const stub = `// Package textkit formats text for URLs and previews.
package textkit

// Slugify turns s into a URL slug.
func Slugify(s string) string { panic("not implemented") }

// Truncate shortens s to at most n runes.
func Truncate(s string, n int) string { panic("not implemented") }
`

// live is one scenario's session, workspace and monitor.
type live struct {
	t       *testing.T
	s       *harness.Session
	m       *monitor
	dir     string
	out     string            // Where the scenario's trace, transitions and machine paths go.
	started time.Duration     // When the latest user message was sent.
	last    message.MessageID // The latest user message.
}

func startLive(t *testing.T, name string, configure ...func(*harness.Config)) *live {
	t.Helper()
	if os.Getenv("STRAP_LIVE_MANAGER") != "1" {
		t.Skip("set STRAP_LIVE_MANAGER=1 to run against the live model endpoint")
	}
	t.Parallel()
	model, profile, err := modelcatalog.Resolve(os.Getenv("STRAP_LIVE_MODELS"), os.Getenv("STRAP_LIVE_PROFILE"), 60*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("STRAP_LIVE_OUTPUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "strap-live", time.Now().Format("20060102-150405"))
	}
	out = filepath.Join(out, name)
	dir := filepath.Join(out, "workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string]string{"go.mod": "module textkit\n\ngo 1.24\n", "README.md": readme, "text.go": stub} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := harness.DefaultConfig()
	cfg.Model, cfg.Dir, cfg.Web = model, dir, nil
	cfg.Events.JSONLPath = filepath.Join(out, "trace.jsonl")
	for _, f := range configure {
		f(&cfg)
	}
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = s.Close(ctx)
		_ = s.Dispose(ctx)
	})
	t.Logf("profile %s at %s; trace and transitions in %s", profile, model.BaseURL, out)
	return &live{t: t, s: s, m: newMonitor(t, s, filepath.Join(out, "transitions.jsonl")), dir: dir, out: out}
}

// send delivers a user message to the manager; kind says what the manager's
// answer to it must do (see machine.Intent).
func (l *live) send(text string, kind machine.Intent) {
	l.t.Helper()
	// Like a person, the test waits until the manager has taken the previous
	// message before typing the next one.
	if l.last != "" {
		l.m.await("the manager to take the previous message", 10*time.Minute, func() bool {
			_, taken := l.m.Consumed[l.last]
			return taken
		})
	}
	l.m.mu.Lock()
	l.started = l.m.since()
	l.m.logLocked(l.m.Turn(l.started, text, kind))
	l.m.mu.Unlock()
	receipt, err := l.s.Send(l.s.Manager(), text)
	if err != nil {
		l.t.Fatal(err)
	}
	l.last = receipt.MessageID
}

// managerWorking waits until a manager owns active or submitted implementation
// work: the moment a user update arrives mid-work.
func (l *live) managerWorking(timeout time.Duration) {
	l.m.await("a manager with implementation work in progress", timeout, func() bool {
		for _, w := range l.m.Works {
			if l.m.Roles[w.Owner] == roster.Manager && (w.Kind == work.Implementation || w.Kind == work.Repair) && !w.State.Terminal() {
				return true
			}
		}
		return false
	})
}

// check runs acceptance tests of its own against the workspace.
func (l *live) check(separator string, truncate bool) {
	l.t.Helper()
	sep := separator
	src := fmt.Sprintf(`package textkit

import "testing"

func TestLiveSlugify(t *testing.T) {
	for in, want := range map[string]string{"Hello, World!": "hello%[1]sworld", "  Go  is fun ": "go%[1]sis%[1]sfun", "a--b": "a%[1]sb", "": ""} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%%q) = %%q, want %%q", in, got, want)
		}
	}
}
`, sep)
	if truncate {
		src += `
func TestLiveTruncate(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"abcdef", 4, "abc…"}, {"abc", 3, "abc"}, {"héllo", 2, "h…"}, {"abc", 0, ""}} {
		if got := Truncate(c.in, c.n); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
`
	}
	path := filepath.Join(l.dir, "zz_live_check_test.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		l.t.Fatal(err)
	}
	defer os.Remove(path)
	cmd := exec.Command("go", "test", "-count=1", "-run", "TestLive", ".")
	cmd.Dir = l.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		l.t.Errorf("workspace fails the acceptance checks: %v\n%s", err, out)
	}
}

// assertHandedToManager: the session has one manager, which the user talks to,
// and it answered the task it was given.
func (l *live) assertHandedToManager() identity.ActorID {
	l.t.Helper()
	m := l.m
	m.mu.Lock()
	defer m.mu.Unlock()
	managers := m.Managers()
	if len(managers) != 1 {
		l.t.Fatalf("want exactly one manager, got %v", managers)
	}
	manager := managers[0]
	if m.Parent[manager] != message.User {
		l.t.Errorf("manager %s has parent %s, want the user", manager, m.Parent[manager])
	}
	if manager != l.s.Manager() {
		l.t.Errorf("manager %s is not the session's manager %s", manager, l.s.Manager())
	}
	for _, turn := range m.Turns {
		if turn.Intent == machine.Task && len(m.RepliesToUser(turn.At)) == 0 {
			l.t.Errorf("the manager never answered the task at %s", turn.At)
		}
	}
	return manager
}

// assertManagerRanWorkers: the manager created its own agents, and every
// implementation it owns was accepted by an auditor it created.
func (l *live) assertManagerRanWorkers(manager identity.ActorID) {
	l.t.Helper()
	m := l.m
	m.mu.Lock()
	defer m.mu.Unlock()
	var workers []string
	auditors := 0
	for id, parent := range m.Parent {
		if parent == manager {
			workers = append(workers, m.Name(id))
			if m.Roles[id] == roster.Auditor {
				auditors++
			}
		}
	}
	if len(workers) == 0 {
		l.t.Fatal("manager created no agents")
	}
	if auditors == 0 {
		l.t.Errorf("manager created no auditor; workers %v", workers)
	}
	implementations := 0
	for _, w := range m.Works {
		if w.Owner != manager {
			continue
		}
		switch w.Kind {
		case work.Implementation:
			implementations++
			if w.State != work.Accepted && w.State != work.Cancelled {
				l.t.Errorf("implementation %s ended %s", w.ID, w.State)
			}
		case work.AuditWork:
			if m.Roles[w.Assignee] != roster.Auditor {
				l.t.Errorf("audit %s assigned to %s", w.ID, m.Name(w.Assignee))
			}
		}
	}
	if implementations == 0 {
		l.t.Error("manager recorded no implementation work")
	}
}

// assertPlanCompleted: the manager planned the task and every step it kept
// completed, each only once an implementation covering it was accepted.
func (l *live) assertPlanCompleted(manager identity.ActorID) {
	l.t.Helper()
	m := l.m
	m.mu.Lock()
	defer m.mu.Unlock()
	owned := 0
	for _, p := range m.Plans {
		if p.Owner != manager {
			l.t.Errorf("plan %s owned by %s", p.ID, m.Name(p.Owner))
			continue
		}
		owned++
		completed := 0
		for _, s := range p.Steps {
			switch s.Status {
			case work.Completed:
				completed++
				at := m.Completed[s.ID]
				covered := false
				for _, w := range m.Works {
					if w.Scope != nil && slices.Contains(w.Scope.StepIDs, s.ID) && w.State == work.Accepted && m.Accepted[w.ID] <= at {
						covered = true
					}
				}
				if !covered {
					l.t.Errorf("step %s %q completed at %s without accepted work covering it", s.ID, s.Title, at)
				}
			case work.CancelledStep:
			default:
				l.t.Errorf("step %s %q ended %s", s.ID, s.Title, s.Status)
			}
		}
		if completed == 0 {
			l.t.Errorf("plan %s completed no steps", p.ID)
		}
	}
	if owned == 0 {
		l.t.Error("manager created no plan")
	}
}

// The user hands an implementation request to the manager; it plans, runs its
// own implementor and auditor, and completes each step as its work is
// accepted.
func TestLiveManagerLifecycle(t *testing.T) {
	l := startLive(t, "lifecycle")
	l.send("Implement Slugify and Truncate in text.go as README.md describes.", machine.Task)
	l.m.awaitSettled(30*time.Minute, 5*time.Second)
	manager := l.assertHandedToManager()
	l.assertManagerRanWorkers(manager)
	l.assertPlanCompleted(manager)
	l.assertMachine()
	l.check("-", true)
}

// A user update sent while the manager is working reaches it at its next turn
// boundary, and the manager folds it into the task.
func TestLiveMidWorkUpdateReachesManager(t *testing.T) {
	l := startLive(t, "update")
	l.send("Implement Slugify in text.go as README.md describes. Leave Truncate alone.", machine.Task)
	l.managerWorking(15 * time.Minute)
	l.send("One more thing: please also implement Truncate from the README, in the same file.", machine.Update)
	updated := l.started
	l.m.awaitSettled(30*time.Minute, 5*time.Second)
	manager := l.assertHandedToManager()
	l.assertForwarded(manager, updated)
	l.assertManagerRanWorkers(manager)
	l.assertPlanCompleted(manager)
	l.assertMachine()
	l.check("-", true)
}

// An update that leaves out what the user wants makes the harness ask the
// user rather than guess; the answer reaches the same manager.
func TestLiveAmbiguousUpdateAsksForClarification(t *testing.T) {
	l := startLive(t, "clarify")
	l.send("Implement Slugify in text.go as README.md describes. Leave Truncate alone.", machine.Task)
	l.managerWorking(15 * time.Minute)
	l.send("Actually, I want Slugify to use a different separator.", machine.Ambiguous)
	asked := l.started
	l.m.await("a clarifying question to the user", 20*time.Minute, func() bool {
		for _, r := range l.m.RepliesToUser(asked) {
			if strings.Contains(r.Content, "?") {
				return true
			}
		}
		return false
	})
	l.send(`Use an underscore: Slugify("Hello, World!") should return "hello_world".`, machine.Answer)
	answered := l.started
	l.m.awaitSettled(30*time.Minute, 5*time.Second)
	manager := l.assertHandedToManager()
	l.assertForwarded(manager, answered)
	l.assertMachine()
	l.check("_", false)
}

// assertForwarded: the user's message at or after after reached the manager,
// which consumed it and then acted on it.
func (l *live) assertForwarded(manager identity.ActorID, after time.Duration) {
	l.t.Helper()
	m := l.m
	m.mu.Lock()
	defer m.mu.Unlock()
	var forwarded *machine.Message
	for i, msg := range m.Messages {
		if msg.At >= after && msg.From == message.User && msg.To == manager && msg.Kind == message.Instruction {
			forwarded = &m.Messages[i]
			break
		}
	}
	if forwarded == nil {
		l.t.Fatalf("the user's message at %s never reached %s", after, manager)
	}
	consumed, ok := m.Consumed[forwarded.ID]
	if !ok {
		l.t.Fatalf("manager never consumed %s", forwarded.ID)
	}
	acted := false
	for _, c := range m.Calls {
		acted = acted || c.Agent == manager && c.At >= consumed
	}
	for _, msg := range m.Messages {
		acted = acted || msg.From == manager && msg.At >= consumed
	}
	if !acted {
		l.t.Errorf("manager consumed %s at %s and did nothing after it", forwarded.ID, consumed)
	}
}

// The whole machine in one session: a chat, a task, a status question while
// the manager works, and a follow-up once it has reported. The chat is
// answered directly without tools, the question from recorded state without
// disturbing the manager, and the task and follow-up go to the session's one
// manager. Every transition is then
// checked against the machine in harness/machine.
func TestLiveStateMachine(t *testing.T) {
	l := startLive(t, "machine")
	l.send("Hi! Before we start: what is a URL slug, in one sentence?", machine.Chat)
	l.m.awaitSettled(10*time.Minute, 3*time.Second) // The manager answers directly.
	l.send("Implement Slugify in text.go as README.md describes. Leave Truncate alone.", machine.Task)
	l.managerWorking(15 * time.Minute)
	l.send("How is it going? What has been done so far?", machine.Question)
	asked := l.started
	l.m.await("the manager to answer the status question", 3*time.Minute, func() bool { return len(l.m.RepliesToUser(asked)) > 0 })
	l.m.awaitSettled(30*time.Minute, 5*time.Second)
	l.send("Thanks. Now please also implement Truncate from the README, in the same file.", machine.FollowUp)
	followed := l.started
	l.m.awaitSettled(30*time.Minute, 5*time.Second)
	manager := l.assertHandedToManager()
	l.assertForwarded(manager, followed)
	l.assertManagerRanWorkers(manager)
	l.assertPlanCompleted(manager)
	l.assertMachine()
	l.check("-", true)
}

// The web belongs to researchers. A task that needs a current fact from
// outside the workspace goes manager → researcher, who reads the web, →
// implementor → auditor, and no other role touches the web. The test checks
// the answer against go.dev itself.
func TestLiveResearchUsesTheWebThroughAResearcher(t *testing.T) {
	l := startLive(t, "research", func(c *harness.Config) { c.Web = &tool.WebConfig{} })
	want := currentGoRelease(t)
	l.send("Create VERSION.txt in the workspace containing only the version string of the latest stable Go release as published on go.dev today, for example go1.2.3. Look it up; do not guess.", machine.Task)
	l.m.awaitSettled(40*time.Minute, 5*time.Second)
	l.assertHandedToManager()
	l.assertMachine()
	m := l.m
	m.mu.Lock()
	delivered, web := 0, 0
	for _, w := range m.Works {
		if w.Kind == work.WebResearch && w.State == work.Delivered {
			delivered++
		}
	}
	for _, c := range m.Calls {
		if (c.Name == "web_search" || c.Name == "open_url") && c.Err == "" {
			web++
		}
	}
	m.mu.Unlock()
	if delivered == 0 {
		t.Error("no research was delivered; the answer did not come from a researcher")
	}
	if web == 0 {
		t.Error("nobody read the web")
	}
	got, err := os.ReadFile(filepath.Join(l.dir, "VERSION.txt"))
	if err != nil || strings.TrimSpace(string(got)) != want {
		t.Errorf("VERSION.txt = %q (%v), want %q", got, err, want)
	}
}

// currentGoRelease reads the latest stable Go release from go.dev.
func currentGoRelease(t *testing.T) string {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://go.dev/VERSION?m=text")
	if err != nil {
		t.Skip("go.dev unreachable:", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Skip("go.dev unreadable:", resp.Status, err)
	}
	return strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
}
