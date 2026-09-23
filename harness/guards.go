package harness

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// rootReplyCheck holds back a root's reply once while the task is not actually
// finished: work it owns is still live, or a plan it owns has open steps that
// no work covers. Steps complete only when an audit accepts work that scoped
// them. In ladder runs roots planned a separate "verify build" step, assigned
// only the implementation, then told the user every step was complete; one
// root assigned that step to a busy implementor, whose assignment was then
// lost, and replied while it was still active. Research-only tasks are exempt
// from the step check, since research never completes plan steps.
func (s *Session) rootReplyCheck(ctx context.Context, root message.ActorID) string {
	view, err := s.workView(ctx)
	if err != nil {
		return ""
	}
	var unfinished []work.Work
	implemented := false
	for _, w := range view.Works() {
		if w.Owner != root {
			continue
		}
		implemented = implemented || w.Kind == work.Implementation || w.Kind == work.Repair
		if !w.State.Terminal() {
			unfinished = append(unfinished, w)
		}
	}
	// Waiting only helps when someone is working: submitted work needs the
	// root's audit, failed audits need its repair, and a stopped assignee will
	// never report. Advising a wait there would stall the root.
	stopped := map[message.ActorID]bool{}
	if len(unfinished) > 0 {
		for _, a := range s.Agents() {
			stopped[a.ID] = a.State == agent.Stopped
		}
	}
	var live []string
	for _, w := range unfinished {
		next := fmt.Sprintf("%s is working on it; wait with wait_for_input", w.Assignee)
		switch {
		case w.State == work.NeedsCheck:
			next = "it is submitted and needs an independent audit; assign one with assign_audit"
		case w.State == work.ChangesRequested:
			next = "its audit requested changes; assign a repair with assign_repair"
		case stopped[w.Assignee]:
			next = fmt.Sprintf("its assignee %s has stopped; reassign it with reassign_work or cancel it with cancel_work", w.Assignee)
		case w.State == work.Checking:
			next = "it is being audited; wait with wait_for_input"
		}
		live = append(live, fmt.Sprintf("%s (%s, %s): %s", w.ID, w.Kind, w.State, next))
	}
	var open []string
	if implemented {
		for _, o := range view.UncoveredSteps(root) {
			open = append(open, fmt.Sprintf("%s in %s %q (%s)", o.Step.ID, o.PlanID, o.Step.Title, o.Step.Status))
		}
	}
	if len(live) == 0 && len(open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Before you reply: the task is not finished.")
	if len(live) > 0 {
		b.WriteString(" Work you own is unfinished: " + capList(live, 10) + ". Cancel work that is no longer needed with cancel_work.")
	}
	if len(open) > 0 {
		b.WriteString(" These plan steps are not complete and no assignment covers them: " + capList(open, 10) + ". A step completes only when an audit accepts implementation work whose scope includes it. Cancel steps the accepted work already covered with cancel_steps, or assign the ones that still need work.")
	}
	b.WriteString(" If this reply is a question for the user or reports a blocker, send it, saying what remains open; otherwise do not report the task complete.")
	return b.String()
}

func capList(items []string, n int) string {
	if len(items) > n {
		items = append(slices.Clone(items[:n]), fmt.Sprintf("and %d more", len(items)-n))
	}
	return strings.Join(items, "; ")
}

const (
	workspaceEntries = 60
	workspaceDepth   = 3
)

// workspaceListing lists dir shallowest first, so a larger tree shows its top
// levels. Agents otherwise guess a layout from the task's wording: told to keep
// "the inventory package" in stock.go, roots read inventory/stock.go in a
// workspace whose files all sit at the top. Hidden entries, version control and
// dependency trees are left out.
func workspaceListing(dir string) *message.Workspace {
	ws := &message.Workspace{Dir: dir}
	level := []string{""}
	for depth := 0; depth < workspaceDepth && len(level) > 0; depth++ {
		var next []string
		for _, rel := range level {
			entries, err := os.ReadDir(filepath.Join(dir, rel))
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					continue
				}
				path := filepath.ToSlash(filepath.Join(rel, name))
				isDir := e.IsDir() || e.Type()&fs.ModeSymlink != 0 && isDirectory(filepath.Join(dir, path))
				if isDir {
					path += "/"
					next = append(next, strings.TrimSuffix(path, "/"))
				}
				if len(ws.Entries) < workspaceEntries {
					ws.Entries = append(ws.Entries, path)
				} else {
					ws.More++
				}
			}
		}
		level = next
	}
	if len(ws.Entries) == 0 {
		return nil
	}
	return ws
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
