package evalcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stevemurr/strap/harness/machine"
	"github.com/stevemurr/strap/harness/replay"
)

const replayUsage = `usage: strap eval replay [-verify-only] [-intents task,chat,...] [-out FILE] [-stall D] TRACE|DIR

Checks a recorded run offline, without the model endpoint. TRACE is a
trace.jsonl; DIR is searched for exactly one.

verify:  the harness state machine over the recording, printing each agent's
         path and every rule it broke. -intents names what each user message
         asked, in order (task, update, answer, follow-up, question, chat, research,
         ambiguous); unnamed messages are checked by the rules every message
         shares.
replay:  a fresh session answering every model call with its recorded output
         and every environment call with its recorded result, while the
         controller, topology, ledger and coordination tools run for real.
         Each model request is compared with the recorded one; the first
         difference per agent is printed. -out keeps the replay's own trace.
`

func replayCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, replayUsage) }
	verifyOnly := fs.Bool("verify-only", false, "Check the recording against the state machine without re-running it")
	intents := fs.String("intents", "", "Comma-separated intent of each user message, in order")
	out := fs.String("out", "", "Write the replay's own trace here")
	stall := fs.Duration("stall", 5*time.Second, "How long to wait for a recorded point before reporting it never reached")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	path, err := findTrace(fs.Arg(0))
	if err != nil {
		return err
	}
	var kinds []machine.Intent
	for _, k := range strings.Split(*intents, ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, machine.Intent(k))
		}
	}
	fmt.Fprintf(stdout, "trace %s\n\n== verify\n", path)
	tl, err := machine.Load(ctx, path, kinds)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, tl.Paths())
	violations := tl.Violations(true)
	for _, v := range violations {
		fmt.Fprintf(stdout, "violation: %s\n", v)
	}
	if len(violations) == 0 {
		fmt.Fprintln(stdout, "no violations")
	}
	failed := len(violations) > 0
	if !*verifyOnly {
		fmt.Fprint(stdout, "\n== replay\n")
		rec, err := replay.Open(ctx, path)
		if err != nil {
			return err
		}
		res, err := replay.Run(ctx, rec, replay.Options{Trace: *out, Stall: *stall})
		if err != nil {
			return err
		}
		for _, n := range res.Notes {
			fmt.Fprintf(stdout, "note: %s\n", n)
		}
		fmt.Fprintf(stdout, "served %d of %d recorded model outputs\n", res.Served, res.Recorded)
		for _, d := range res.Divergences {
			fmt.Fprintf(stdout, "divergence: %s\n", d)
		}
		if res.Faithful() {
			fmt.Fprintln(stdout, "faithful: every request matched the recording")
		}
		failed = failed || !res.Faithful()
	}
	if failed {
		return errors.New("the run broke the machine or did not replay faithfully")
	}
	return nil
}

// findTrace resolves a trace.jsonl or the one trace under a directory.
func findTrace(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	var found []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "trace.jsonl" {
			found = append(found, p)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no trace.jsonl under %s", path)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d traces under %s; name one:\n  %s", len(found), path, strings.Join(found, "\n  "))
}
