// Strap-eval runs the harness against the task ladder under eval/ladder,
// records one JSONL trace per task, grades each workspace with hidden tests,
// and summarizes the traces.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/modelcatalog"
)

const usage = `usage:
  strap-eval run       [-ladder DIR] [-out DIR] [-tier T] [-task ID,...] [-parallel N] [model flags]
  strap-eval selfcheck [-ladder DIR] [-tier T] [-task ID,...] [-parallel N]
  strap-eval list      [-ladder DIR] [-tier T] [-task ID,...]
  strap-eval report    RUN_DIR

run records each task under RUN_DIR/<task>/ (trace.jsonl, workspace/, result.json)
and appends RUN_DIR/results.jsonl; rerun with the same -out to resume.
selfcheck proves every hidden test fails on the stub and passes on the reference.
report reads a run directory and writes report.md and report.json beside it.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "strap-eval:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "run":
		return runCmd(ctx, args[1:], stdout, stderr)
	case "selfcheck":
		return selfcheckCmd(ctx, args[1:], stdout, stderr)
	case "list":
		return listCmd(args[1:], stdout, stderr)
	case "report":
		return reportCmd(ctx, args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

type selection struct {
	ladder string
	tier   string
	tasks  string
}

func (s *selection) flags(fs *flag.FlagSet) {
	fs.StringVar(&s.ladder, "ladder", "eval/ladder", "Task ladder directory")
	fs.StringVar(&s.tier, "tier", "", "Only run this tier (easy, medium or hard)")
	fs.StringVar(&s.tasks, "task", "", "Only run these comma-separated task ids")
}

func (s *selection) filter() func(eval.Task) bool {
	ids := map[string]bool{}
	for _, id := range strings.Split(s.tasks, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	return func(t eval.Task) bool {
		if s.tier != "" && t.Tier != s.tier {
			return false
		}
		return len(ids) == 0 || ids[t.ID]
	}
}

func (s *selection) load() ([]eval.Task, error) {
	tasks, err := eval.LoadLadder(s.ladder)
	if err != nil {
		return nil, err
	}
	keep := s.filter()
	var out []eval.Task
	for _, t := range tasks {
		if keep(t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no tasks selected")
	}
	return out, nil
}

func runCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap-eval run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sel selection
	sel.flags(fs)
	out := fs.String("out", "", "Run directory (default eval/results/<timestamp>)")
	parallel := fs.Int("parallel", 1, "Concurrent sessions")
	quiet := fs.Duration("quiet", 3*time.Second, "Silence required after the root's final reply before a task is considered finished")
	idle := fs.Duration("idle", 3*time.Minute, "Silence with every agent idle and no root reply after which a task is finished and flagged no_reply")
	scratch := fs.String("scratch", "", "Parent directory for live workspaces while sessions run (default the system temp directory)")
	configPath := fs.String("config", "", "Model catalog JSON (default $XDG_CONFIG_HOME/strap/models.json or ~/.config/strap/models.json, then bundled catalog)")
	profile := fs.String("profile", "", "Saved model profile (default selected by the catalog)")
	cfg := harness.DefaultConfig()
	cfg.Model = harness.ModelConfig{Backend: "vllm", Preset: "none", Timeout: cfg.Model.Timeout}
	modelcatalog.Flags(fs, &cfg.Model)
	fs.IntVar(&cfg.ReasoningLimit, "reasoning-limit", cfg.ReasoningLimit, "Reasoning bytes a model call may stream before it is cut off and retried once (0 disables)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	model, err := modelcatalog.Load(*configPath, *profile, cfg.Model.Timeout)
	if err != nil {
		return err
	}
	if modelcatalog.WasSet(fs, "preset") || (modelcatalog.WasSet(fs, "backend") && cfg.Model.Backend == "chatcompletions") {
		model.Preset = "none"
		model.Generation = harness.ModelConfig{}.Generation
	}
	cfg.Model = model
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := cfg.Model.NewProvider(nil); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join("eval", "results", time.Now().Format("20060102-150405"))
	}
	fmt.Fprintf(stderr, "model %s at %s; results in %s\n", cfg.Model.Model, cfg.Model.BaseURL, *out)
	results, err := eval.Run(ctx, eval.Options{Config: cfg, Ladder: sel.ladder, Output: *out, Parallel: *parallel, Filter: sel.filter(), Log: stderr, Quiet: *quiet, Idle: *idle, Scratch: *scratch})
	if len(results) > 0 {
		passed := 0
		for _, r := range results {
			if r.Passed {
				passed++
			}
		}
		fmt.Fprintf(stdout, "%d/%d passed; run: strap-eval report %s\n", passed, len(results), *out)
	}
	return err
}

func selfcheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap-eval selfcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sel selection
	sel.flags(fs)
	parallel := fs.Int("parallel", max(1, runtime.NumCPU()/2), "Concurrent go test invocations")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tasks, err := sel.load()
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "strap-eval-selfcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	checks := eval.SelfCheck(ctx, tasks, *parallel, scratch)
	bad := 0
	for _, c := range checks {
		switch {
		case c.Error != "":
			bad++
			fmt.Fprintf(stdout, "ERROR %s: %s\n", c.TaskID, c.Error)
		case !c.StubFails:
			bad++
			fmt.Fprintf(stdout, "WEAK  %s: hidden tests pass against the stub\n", c.TaskID)
		case !c.Reference.Passed:
			bad++
			fmt.Fprintf(stdout, "FAIL  %s: reference does not pass hidden tests\n%s\n", c.TaskID, c.Reference.Output)
		default:
			fmt.Fprintf(stdout, "ok    %s (%s)\n", c.TaskID, c.Reference.Duration.Round(time.Millisecond))
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d tasks failed selfcheck", bad, len(checks))
	}
	fmt.Fprintf(stdout, "%d tasks ok\n", len(checks))
	return nil
}

func listCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap-eval list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sel selection
	sel.flags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	tasks, err := sel.load()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		fmt.Fprintf(stdout, "%-8s %-40s %s — %s\n", t.Tier, t.ID, t.Title, t.Insight)
	}
	return nil
}

func reportCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap-eval report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("report needs one run directory")
	}
	rep, err := eval.Analyze(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if err := eval.WriteReport(rep); err != nil {
		return err
	}
	fmt.Fprint(stdout, rep.Markdown())
	return nil
}
