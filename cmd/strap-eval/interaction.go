package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/eval/interaction"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/lspconfig"
	"github.com/stevemurr/strap/internal/modelcatalog"
)

const interactionUsage = `usage:
  strap-eval interaction list
  strap-eval interaction run [-mode scripted|live] [-scenario ID,...] [-repeat N] [-out DIR] [-max-calls N] [-max-tool-calls N] [-timeout DURATION] [model flags]
  strap-eval interaction report RUN_DIR

scripted is the default and needs no model, catalog, or network connection.
live runs one model actor with controlled collaborators, sequentially.
Harness correctness and script/model behavior are reported separately.
Schema cases also report first tool choice, first argument validity, and invalid calls.
The default limits are 8 model calls, 24 tool calls, and 3 minutes per trial.
Use -model-timeout for individual model requests; -timeout bounds the whole trial.
Run directories must be empty; this pilot does not resume existing runs.
`

func interactionCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, interactionUsage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("strap-eval interaction list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("interaction list takes no arguments")
		}
		for _, scenario := range interaction.List() {
			fmt.Fprintf(stdout, "%s\tv%d\t%s\n", scenario.ID, scenario.Version, scenario.Title)
		}
		return nil
	case "run":
		return interactionRunCmd(ctx, args[1:], stdout, stderr)
	case "report":
		return interactionReportCmd(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, interactionUsage)
		return nil
	default:
		fmt.Fprint(stderr, interactionUsage)
		return fmt.Errorf("unknown interaction command %q", args[0])
	}
}

func interactionOptions(args []string, stderr io.Writer) (interaction.Options, error) {
	fs := flag.NewFlagSet("strap-eval interaction run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", string(interaction.Scripted), "Execution mode: scripted or live")
	scenarios := fs.String("scenario", "", "Only run these comma-separated scenario ids (default all)")
	opts := interaction.Options{Config: harness.DefaultConfig()}
	languageFlags := lspconfig.Flags(fs)
	fs.StringVar(&opts.Output, "out", "", "Empty run directory (default eval/results/interaction_<commit>_<profile>_<timestamp>)")
	fs.IntVar(&opts.Repetitions, "repeat", 1, "Sequential trials per scenario")
	fs.IntVar(&opts.MaxCalls, "max-calls", 8, "Maximum model calls per trial")
	fs.IntVar(&opts.MaxToolCalls, "max-tool-calls", 24, "Maximum tool calls per trial")
	fs.DurationVar(&opts.Timeout, "timeout", 3*time.Minute, "Wall-clock limit per trial")
	configPath := fs.String("config", "", "Model catalog JSON (live mode only; same discovery as ladder)")
	profile := fs.String("profile", "", "Saved model profile (live mode only; default selected by catalog)")
	opts.Config.Model = harness.ModelConfig{Backend: "vllm", Timeout: opts.Config.Model.Timeout}
	// Keep the common model flags, reserving -timeout for the trial boundary.
	modelFlags := flag.NewFlagSet("model", flag.ContinueOnError)
	modelcatalog.Flags(modelFlags, &opts.Config.Model)
	modelFlags.VisitAll(func(f *flag.Flag) {
		name := f.Name
		if name == "timeout" {
			name = "model-timeout"
		}
		fs.Var(f.Value, name, f.Usage)
	})
	fs.IntVar(&opts.Config.ReasoningLimit, "reasoning-limit", opts.Config.ReasoningLimit, "Reasoning bytes a model call may stream before it is cut off and retried once (0 disables)")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected interaction run arguments: %s", strings.Join(fs.Args(), " "))
	}
	opts.Mode = interaction.Mode(*mode)
	if opts.Mode != interaction.Scripted && opts.Mode != interaction.Live {
		return opts, fmt.Errorf("invalid mode %q: use scripted or live", *mode)
	}
	if opts.Repetitions < 1 || opts.MaxCalls < 1 || opts.MaxToolCalls < 1 || opts.Timeout <= 0 {
		return opts, errors.New("repeat, max-calls, max-tool-calls, and timeout must be positive")
	}
	if *scenarios != "" {
		for _, id := range strings.Split(*scenarios, ",") {
			opts.ScenarioIDs = append(opts.ScenarioIDs, strings.TrimSpace(id))
		}
	}
	opts.Profile = "scripted"
	if opts.Mode == interaction.Live {
		profileName, err := modelcatalog.Apply(fs, args, &opts.Config.Model, *configPath, *profile)
		if err != nil {
			return opts, err
		}
		opts.Profile = profileName
	}
	languages, err := languageFlags.Resolve()
	if err != nil {
		return opts, err
	}
	opts.Config.LSP = languages
	opts.Commit = eval.BuildCommit()
	if opts.Output == "" {
		opts.Output = filepath.Join("eval", "results", "interaction_"+eval.RunName(opts.Commit, opts.Profile, time.Now()))
	}
	return opts, nil
}

func interactionRunCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := interactionOptions(args, stderr)
	if err != nil {
		return err
	}
	opts.Observe = func(result interaction.Result) { printInteractionResult(stdout, result) }
	report, runErr := interaction.Run(ctx, opts)
	if len(report.Results) != 0 || (runErr != nil && report.PlannedTrials > 0) {
		if runErr == nil {
			printInteractionSummary(stdout, opts.Output, report)
		} else if report.PlannedTrials > 0 {
			fmt.Fprintf(stdout, "run stopped; %d/%d trials recorded; results in %s\n", len(report.Results), report.PlannedTrials, opts.Output)
		} else {
			fmt.Fprintf(stdout, "run stopped; %d completed trial results in %s\n", len(report.Results), opts.Output)
		}
	}
	if runErr != nil {
		return runErr
	}
	return interactionOutcomeError(report)
}

func interactionReportCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("strap-eval interaction report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("interaction report requires one run directory")
	}
	dir := fs.Arg(0)
	report, err := interaction.ReadReport(dir)
	if err != nil {
		return err
	}
	if len(report.Results) == 0 {
		return interactionOutcomeError(report)
	}
	if err := interaction.WriteReport(dir, report); err != nil {
		return err
	}
	for _, result := range report.Results {
		printInteractionResult(stdout, result)
	}
	printInteractionSummary(stdout, dir, report)
	return interactionOutcomeError(report)
}

func printInteractionResult(out io.Writer, result interaction.Result) {
	actor := "script"
	if result.Mode == interaction.Live {
		actor = "model"
	}
	fmt.Fprintf(out, "%s trial=%d %s; harness=%d/%d; %s behavior: scorable=%t correct=%t clean=%t recovery=%t rejected=%d output_errors=%d unknown_errors=%d; calls=%d tools=%d",
		result.ScenarioID, result.Trial, result.Outcome, result.Harness.Passed, result.Harness.Total,
		actor, result.Behavior.Scorable, result.Behavior.OutcomeCorrect, result.Behavior.CleanSuccess,
		result.Behavior.RecoverySuccess, result.Behavior.RejectedCalls, result.Behavior.OutputErrors, result.Behavior.UnknownToolErrors,
		result.ModelCalls, result.ToolCalls)
	if s := result.Schema; s != nil {
		switch {
		case !result.Behavior.Scorable:
			fmt.Fprint(out, "; schema: first_decision=excluded")
		case s.Attempts == 0:
			fmt.Fprint(out, "; schema: first_decision=unattempted")
		default:
			fmt.Fprintf(out, "; schema: first_tool=%t first_arguments=%t", s.FirstToolCorrect, s.FirstArgumentsValid)
		}
		fmt.Fprintf(out, " invalid=%d/%d", s.InvalidCalls, s.Attempts)
	}
	fmt.Fprintln(out)
}

func printInteractionSummary(out io.Writer, dir string, report interaction.Report) {
	passed := 0
	for _, result := range report.Results {
		if result.Outcome == "passed" {
			passed++
		}
	}
	fmt.Fprintf(out, "%d/%d interaction trials passed (mode=%s); results in %s\n", passed, len(report.Results), report.Mode, dir)
	if report.PlannedTrials > 0 {
		fmt.Fprintf(out, "%d/%d trials recorded\n", len(report.Results), report.PlannedTrials)
	}
	fmt.Fprintf(out, "reports: %s, %s\n", filepath.Join(dir, "report.md"), filepath.Join(dir, "report.json"))
}

func interactionOutcomeError(report interaction.Report) error {
	if report.PlannedTrials > 0 && len(report.Results) < report.PlannedTrials {
		return fmt.Errorf("interaction run is incomplete: %d/%d trials recorded", len(report.Results), report.PlannedTrials)
	}
	if len(report.Results) == 0 {
		return errors.New("interaction run is incomplete: no completed trial results")
	}
	failed := 0
	for _, result := range report.Results {
		if result.Outcome != "passed" {
			failed++
		}
	}
	if failed != 0 {
		return fmt.Errorf("%d/%d interaction trials did not pass", failed, len(report.Results))
	}
	return nil
}
