package harness

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/tool"
)

// environmentRecord publishes every call of a tool that reaches outside the
// session with the result the environment returned, before the harness wraps
// it for the model. It sits outermost around the environment, so the record
// holds exactly what the harness received, including language feedback and
// any recorded results a replay substituted. Those records are what makes a
// trace replayable: the environment is the one part a replay cannot rerun.
type environmentRecord struct {
	tool.Tool
	s   *Session
	dir string // The workspace, compared before and after calls that can write.
}

func (s *Session) recordEnvironment(t tool.Tool) tool.Tool {
	dir, _ := filepath.Abs(s.config.Dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return &environmentRecord{Tool: t, s: s, dir: dir}
}

// writers are the environment tools that can change the workspace. A shell
// command names no files, so which ones it wrote is only known by comparing
// the workspace before and after it.
var writers = map[string]bool{"write_file": true, "edit_file": true, "shell": true, "run_trials": true}

// RecordedChanges is implemented by replay stand-ins, which report the
// changes the recorded call made instead of the recorder scanning for them.
type RecordedChanges interface {
	RecordedChanges(invocation string) (changed []string, unscanned, scanned, ok bool)
}

func (t *environmentRecord) InputContract() tool.Contract {
	if typed, ok := t.Tool.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (t *environmentRecord) Validate() error { return tool.ValidateTool(t.Tool) }
func (t *environmentRecord) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (t *environmentRecord) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	name := t.Definition().Name
	var before map[string]fileStamp
	var complete bool
	recorded, replayed := t.Tool.(RecordedChanges)
	scan := writers[name] && !replayed && t.dir != ""
	if scan {
		before, complete = snapshot(t.dir)
	}
	result, err := t.Tool.Call(ctx, c)
	e := conversation.EnvironmentEvent{Agent: c.Actor, InvocationID: c.InvocationID, Name: name, Arguments: c.Arguments, Content: result.Content.Clone(), Captured: result.Captured.Clone()}
	if err != nil {
		e.Err = err.Error()
	}
	switch {
	case replayed:
		e.Changed, e.Unscanned, e.Scanned, _ = recorded.RecordedChanges(c.InvocationID)
	case scan:
		after, done := snapshot(t.dir)
		if complete && done {
			e.Changed, e.Scanned = changedFiles(before, after), true
		} else {
			e.Unscanned = true
		}
	}
	if c.Actor != "" && c.InvocationID != "" {
		_ = t.s.publish(e) // A failed publish fails the log, which stops the session.
	}
	t.s.noteChanges(c.Actor, e.Changed)
	outcome := e.Captured.Text()
	if outcome == "" {
		outcome = e.Content.Text()
	}
	t.s.noteRun(c.Actor, name, c.Arguments, outcome)
	return result, err
}

type fileStamp struct {
	size int64
	mod  int64 // Nanoseconds; comparable, unlike time.Time.
	mode fs.FileMode
}

// snapshotLimit bounds the files compared per call; larger workspaces are
// reported unscanned rather than slowing every write.
const snapshotLimit = 20000

// snapshot stamps every file under dir, skipping .git; complete is false when
// the limit was reached.
func snapshot(dir string) (files map[string]fileStamp, complete bool) {
	files, complete = map[string]fileStamp{}, true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= snapshotLimit {
			complete = false
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		files[filepath.ToSlash(rel)] = fileStamp{size: info.Size(), mod: info.ModTime().UnixNano(), mode: info.Mode()}
		return nil
	})
	return files, complete
}

// changedFiles lists the paths created, modified or deleted between two
// snapshots, sorted.
func changedFiles(before, after map[string]fileStamp) []string {
	var out []string
	for p, a := range after {
		if b, ok := before[p]; !ok || b != a {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}
