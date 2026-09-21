// Package evalcmd implements the strap eval command group. It runs one coding
// problem per container and publishes its workspace to an outbox; a separate
// grade command consumes submissions with hidden tests.
package evalcmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/evalweb"
	"github.com/stevemurr/strap/internal/evalwire"
	"github.com/stevemurr/strap/internal/modelflags"
	"github.com/stevemurr/strap/internal/tui"
	"golang.org/x/term"
)

const usage = `usage:
  strap eval -problem ID [-q] [model flags]
  strap eval run -problem ID [-ui auto|tui|plain|quiet] [-q] [model flags]
  strap eval selfcheck [-tier easy,medium,hard] [-task ID,...] [-parallel N]
  strap eval list [-tier easy,medium,hard] [-task ID,...]
  strap eval grade [-q]
  strap eval report
  strap eval web [-results DIR] [-listen ADDR]
  strap eval interaction list|run|report [options]

Run one coding problem per container. Public fixtures live at /problems.
Use an empty /workspace, and mount empty directories at /results and /outbox.
The agent publishes /outbox/submission after closing; it never grades its work.
grade reads /outbox (read-only), /grading (private task ladder, read-only), and
/results from that attempt, using a fresh /workspace for hidden tests.
Retries require fresh mounts. Launch separate containers for parallel problems.
-q suppresses the TUI and progress logs, retaining the final summary and errors.
selfcheck proves hidden tests fail on the stub and pass on the reference.
interaction is the separate bounded coordination suite; use interaction -help.
web serves a local page for reading and comparing the runs under a results
directory (default eval/results); it reads results.jsonl and traces directly.
Starting runs from the page requires Apple's container CLI. It builds the eval
image by default and launches separate agent and grader containers. Use
-no-build to reuse an image, -image to select it, and -build-context for its source.
`

// Main dispatches the eval command group. It returns flag.ErrHelp after
// printing usage when no command is given.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "run":
		return runCmd(ctx, args[1:], stdout, stderr)
	case "grade":
		return gradeCmd(ctx, args[1:], stdout, stderr, eval.ContainerMounts())
	case "selfcheck":
		return selfcheckCmd(ctx, args[1:], stdout, stderr)
	case "list":
		return listCmd(args[1:], stdout, stderr)
	case "report":
		return reportCmd(ctx, args[1:], stdout, stderr)
	case "web":
		return webCmd(ctx, args[1:], stdout, stderr)
	case "interaction":
		return interactionCmd(ctx, args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	if strings.HasPrefix(args[0], "-") {
		return runCmd(ctx, args, stdout, stderr)
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
	s.ladder = eval.ContainerMounts().Problems
	fs.StringVar(&s.tier, "tier", "", "Comma-separated tiers: easy,medium,hard (default all)")
	fs.StringVar(&s.tasks, "task", "", "Only run these comma-separated task ids")
}

func (s *selection) filter() func(eval.Task) bool {
	tiers := map[string]bool{}
	for _, tier := range strings.Split(s.tier, ",") {
		if tier = strings.TrimSpace(tier); tier != "" {
			tiers[tier] = true
		}
	}
	ids := map[string]bool{}
	for _, id := range strings.Split(s.tasks, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	return func(t eval.Task) bool {
		if len(tiers) != 0 && !tiers[t.Tier] {
			return false
		}
		return len(ids) == 0 || ids[t.ID]
	}
}

func (s *selection) validate() error {
	if s.tier == "" {
		return nil
	}
	for _, tier := range strings.Split(s.tier, ",") {
		switch strings.TrimSpace(tier) {
		case "easy", "medium", "hard":
		default:
			return fmt.Errorf("invalid tier %q: use easy, medium, or hard", strings.TrimSpace(tier))
		}
	}
	return nil
}

func (s *selection) load() ([]eval.Task, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	tasks, err := eval.LoadProblems(s.ladder)
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
	return runMounted(ctx, args, stdout, stderr, eval.ContainerMounts())
}

func runMounted(ctx context.Context, args []string, stdout, stderr io.Writer, mounts eval.Mounts) error {
	fs := flag.NewFlagSet("strap eval run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runConfig := fs.String("run-config", "", "Resolved eval configuration snapshot (container launcher)")
	progressJSON := fs.Bool("progress-json", false, "Stream structured progress on stdout (container launcher)")
	problem := fs.String("problem", "", "Problem ID (one per container)")
	ui := fs.String("ui", "auto", "Progress display: auto, tui, plain, or quiet")
	quietMode := fs.Bool("q", false, "Disable the TUI and progress logs (overrides -ui with quiet)")
	report := fs.Bool("report", true, "Write an ungraded execution report on completion")
	quiet := fs.Duration("quiet", 3*time.Second, "Silence required after the root's final reply before a task is considered finished")
	idle := fs.Duration("idle", 3*time.Minute, "Silence with every agent idle and no root reply after which a task is finished and flagged no_reply")
	cfg := harness.DefaultConfig()
	model := modelflags.Register(fs, &cfg, "timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected run arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(*problem) == "" {
		return errors.New("-problem is required: run one problem per container")
	}
	if *progressJSON {
		*quietMode = true
	}
	displayMode := *ui
	if *quietMode {
		displayMode = "quiet"
	}
	interactive, err := useTUI(displayMode, os.Stdin, stdout)
	if err != nil {
		return err
	}
	var profileName string
	if *runConfig != "" {
		// A snapshot is already resolved on the host. Mixing catalog/model flags
		// would silently change the evaluated model or generation policy.
		allowed := map[string]bool{"run-config": true, "progress-json": true, "problem": true, "q": true, "ui": true, "report": true, "quiet": true, "idle": true}
		var conflict string
		fs.Visit(func(f *flag.Flag) {
			if !allowed[f.Name] {
				conflict = f.Name
			}
		})
		if conflict != "" {
			return fmt.Errorf("-run-config cannot be combined with -%s", conflict)
		}
		data, readErr := os.ReadFile(*runConfig)
		if readErr != nil {
			return readErr
		}
		snapshot, parseErr := evalwire.ParseConfig(data)
		if parseErr != nil {
			return parseErr
		}
		cfg, profileName = snapshot.Harness, snapshot.Profile
	} else {
		profileName, err = model.Model(args)
		if err != nil {
			return err
		}
		if err := model.Languages(); err != nil {
			return err
		}
	}
	// Human summaries never mix with the stdout protocol.
	progressOut := stdout
	if *progressJSON {
		stdout = stderr
	}
	commit := eval.BuildCommit()
	log := stderr
	if displayMode == "quiet" {
		log = io.Discard
	}
	fmt.Fprintf(log, "model %s at %s; results in %s\n", cfg.Model.Model, cfg.Model.BaseURL, mounts.Results)
	opts := eval.Options{Config: cfg, Mounts: mounts, Problem: *problem, Log: log, Quiet: *quiet, Idle: *idle, Commit: commit, Profile: profileName}
	var progressMu sync.Mutex
	var progressErr error
	if *progressJSON {
		encoder := json.NewEncoder(progressOut)
		opts.Observe = func(p eval.Progress) {
			record, emit, e := evalwire.FromProgress(p)
			progressMu.Lock()
			defer progressMu.Unlock()
			if progressErr != nil {
				return
			}
			if e != nil {
				progressErr = e
				return
			}
			if emit {
				progressErr = encoder.Encode(record)
			}
		}
	}
	var results []eval.Result
	if interactive {
		results, err = tui.RunEval(ctx, opts, os.Stdin, stdout)
	} else {
		results, err = eval.Run(ctx, opts)
	}
	progressMu.Lock()
	err = errors.Join(err, progressErr)
	progressMu.Unlock()
	if len(results) > 0 {
		submitted := 0
		for _, r := range results {
			if r.Outcome == eval.Submitted {
				submitted++
			}
		}
		if err != nil {
			fmt.Fprintf(stdout, "run stopped; artifacts retained in %s (retry with fresh mounts)\n", mounts.Results)
		} else {
			fmt.Fprintf(stdout, "%d/%d submitted to %s; results in %s\n", submitted, len(results), mounts.Outbox, mounts.Results)
		}
	}
	if err == nil && *report {
		fmt.Fprintln(log, "writing reports…")
		rep, reportErr := eval.Analyze(ctx, mounts.Results)
		if reportErr == nil {
			reportErr = eval.WriteReport(rep)
		}
		if reportErr != nil {
			return fmt.Errorf("write report: %w", reportErr)
		}
		fmt.Fprintf(stdout, "reports: %s, %s\n", filepath.Join(mounts.Results, "report.md"), filepath.Join(mounts.Results, "report.json"))
	}
	return err
}

func useTUI(mode string, input io.Reader, output io.Writer) (bool, error) {
	isTerminal := func(v any) bool { f, ok := v.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }
	switch mode {
	case "auto":
		ci := os.Getenv("CI")
		return isTerminal(input) && isTerminal(output) && os.Getenv("TERM") != "dumb" && (ci == "" || ci == "false" || ci == "0"), nil
	case "plain", "quiet":
		return false, nil
	case "tui":
		if !isTerminal(input) || !isTerminal(output) {
			return false, errors.New("-ui tui requires terminal input and output; use -ui plain for redirected output")
		}
		return true, nil
	default:
		return false, fmt.Errorf("invalid ui %q: use auto, tui, plain, or quiet", mode)
	}
}

func selfcheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap eval selfcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sel selection
	sel.flags(fs)
	sel.ladder = eval.ContainerMounts().Grading
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
	fs := flag.NewFlagSet("strap eval list", flag.ContinueOnError)
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
	fs := flag.NewFlagSet("strap eval report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("report reads /results and takes no arguments")
	}
	rep, err := eval.Analyze(ctx, eval.ContainerMounts().Results)
	if err != nil {
		return err
	}
	if err := eval.WriteReport(rep); err != nil {
		return err
	}
	fmt.Fprint(stdout, rep.Markdown())
	return nil
}

// gradeCmd does not initialize a model or launch an agent.
func gradeCmd(ctx context.Context, args []string, stdout, stderr io.Writer, mounts eval.Mounts) error {
	fs := flag.NewFlagSet("strap eval grade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("q", false, "Suppress progress logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("grade takes no arguments; it consumes /outbox/submission")
	}
	if !*quiet {
		fmt.Fprintln(stderr, "grading submitted workspace…")
	}
	r, err := eval.GradeSubmission(ctx, mounts)
	if err != nil {
		return err
	}
	rep, err := eval.Analyze(ctx, mounts.Results)
	if err != nil {
		return err
	}
	if err := eval.WriteReport(rep); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %s; results in %s\n", r.TaskID, r.Outcome, mounts.Results)
	return nil
}

func webCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap eval web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	results := fs.String("results", filepath.Join("eval", "results"), "Directory holding run directories (any depth)")
	listen := fs.String("listen", "127.0.0.1:0", "Loopback address to serve on; port 0 picks a free port")
	image := fs.String("image", "strap-eval", "Agent/grader container image")
	noBuild := fs.Bool("no-build", false, "Use the existing eval image instead of building it")
	buildContext := fs.String("build-context", "", "Eval image build context (default repository containing the ladder)")
	ladder := fs.String("ladder", filepath.Join("eval", "ladder"), "Private task ladder used to run and grade from the page; missing disables running")
	quiet := fs.Duration("quiet", 3*time.Second, "Silence required after the root's final reply before a task is considered finished")
	idle := fs.Duration("idle", 3*time.Minute, "Silence with every agent idle and no root reply after which a task is finished and flagged no_reply")
	cfg := harness.DefaultConfig()
	model := modelflags.Register(fs, &cfg, "timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected web arguments: %s", strings.Join(fs.Args(), " "))
	}
	var runner *evalweb.Runner
	if info, err := os.Stat(*ladder); err == nil && info.IsDir() {
		profileName, err := model.Model(args)
		if err != nil {
			return err
		}
		if err := model.Languages(); err != nil {
			return err
		}
		ladderPath, err := filepath.Abs(*ladder)
		if err != nil {
			return err
		}
		runner = &evalweb.Runner{Config: cfg, Ladder: ladderPath, Profile: profileName, Commit: eval.BuildCommit(), Quiet: *quiet, Idle: *idle, Image: *image, NoBuild: *noBuild, BuildContext: *buildContext}
	} else {
		fmt.Fprintf(stderr, "no ladder at %s; running from the page is disabled\n", *ladder)
	}
	server, err := evalweb.New(*results, runner)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	if !listener.Addr().(*net.TCPAddr).IP.IsLoopback() {
		listener.Close()
		return errors.New("web serves without authentication; listen on a loopback address")
	}
	fmt.Fprintf(stdout, "serving %s at http://%s/\n", server.Root(), listener.Addr())
	if runner != nil {
		fmt.Fprintf(stdout, "runs launched from the page use model %s at %s and execute on this host\n", runner.Config.Model.Model, runner.Config.Model.BaseURL)
	}
	httpServer := &http.Server{Handler: server}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
