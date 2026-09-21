package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeContainer = `#!/usr/bin/env bash
set -e
printf '%s\t' "$@" >> "$CALLS"
printf '\n' >> "$CALLS"
if [[ "$1" != run ]]; then exit 0; fi
shift
outbox=''; results=''; grading=''
while [[ $# -gt 0 ]]; do
 case "$1" in
  --mount)
   mount=$2
   source=${mount#*source=}; source=${source%%,target=*}
   target=${mount#*target=}; target=${target%%,*}
   case "$target" in
    /outbox) outbox=$source ;;
    /results) results=$source ;;
    /grading) grading=$source ;;
   esac
   shift 2 ;;
  strap-eval) shift; break ;;
  *) shift ;;
 esac
done
case "$1" in
 list)
  printf 'easy easy-01-probe Probe\n'
  if [[ "$*" != *'-tier easy'* ]]; then printf 'medium medium-01-probe Probe\n'; fi
  ;;
 grade)
  [[ -n "$grading" && -f "$outbox/submission/manifest.json" ]]
  [[ "$FAIL_GRADE" != 1 ]] || exit 9
  printf 'passed\n' > "$results/report.md"
  echo 'probe: passed; results in /results'
  ;;
 -problem)
  [[ -z "$grading" ]] || exit 8
  [[ "$2" != "$FAIL_AGENT" ]] || exit 7
  mkdir -p "$outbox/submission"
  printf '{}\n' > "$outbox/submission/manifest.json"
  echo '1/1 submitted'
  ;;
 *) exit 10 ;;
esac
`

func launcher(t *testing.T) (string, string, []string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"scripts", "eval/ladder", "bin", "catalog/strap"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("eval.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts/eval.sh")
	for path, body := range map[string][]byte{
		script:                               data,
		filepath.Join(root, "bin/container"): []byte(fakeContainer),
		filepath.Join(root, "catalog/strap/models.json"):   []byte(`{"default":"test","models":{}}`),
		filepath.Join(root, "eval/ladder/private-fixture"): []byte("private"),
	} {
		if err := os.WriteFile(path, body, 0755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"), "CALLS="+filepath.Join(root, "calls"), "XDG_CONFIG_HOME="+filepath.Join(root, "catalog"), "FAIL_AGENT=", "FAIL_GRADE=")
	return root, script, env
}

func invoke(t *testing.T, script string, env []string, args ...string) (string, error) {
	t.Helper()
	// macOS ships Bash 3.2: avoid accidentally depending on Homebrew Bash features.
	cmd := exec.Command("/bin/bash", append([]string{script}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestLauncherMountsAndModelArguments(t *testing.T) {
	root, script, env := launcher(t)
	out := filepath.Join(root, "batch with spaces")
	output, err := invoke(t, script, env, "--out", out, "easy-01-probe", "--", "-profile", "fixture profile")
	if err != nil {
		t.Fatal(output, err)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	if !strings.HasPrefix(text, "build\t") || !strings.Contains(text, "-profile\tfixture profile") {
		t.Fatal(text)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 4 {
		t.Fatal(text)
	}
	agent, grader := lines[2], lines[3]
	if strings.Contains(agent, "target=/grading") || !strings.Contains(agent, "target=/config,readonly") {
		t.Fatal(agent)
	}
	if !strings.Contains(grader, "--network\tnone") || !strings.Contains(grader, "target=/outbox,readonly") || !strings.Contains(grader, "target=/grading,readonly") || strings.Contains(grader, "target=/workspace") {
		t.Fatal(grader)
	}
	if _, err := os.Stat(filepath.Join(out, "easy-01-probe/results/report.md")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, filepath.Join(out, "easy-01-probe/results/report.md")) {
		t.Fatal(output)
	}
}

func TestLauncherContinuesAfterAgentFailureWithoutGradingIt(t *testing.T) {
	root, script, env := launcher(t)
	env = append(env, "FAIL_AGENT=easy-01-probe")
	out := filepath.Join(root, "batch")
	output, err := invoke(t, script, env, "--no-build", "--out", out, "--all")
	if err == nil {
		t.Fatal("lost infrastructure failure", output)
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls"))
	if strings.Contains(string(calls), "build\t") || strings.Count(string(calls), "grade\t-q") != 1 {
		t.Fatal(string(calls))
	}
	if _, err := os.Stat(filepath.Join(out, "medium-01-probe/results/report.md")); err != nil {
		t.Fatal(output, err)
	}
	if _, err := os.Stat(filepath.Join(out, "easy-01-probe/grader.log")); !os.IsNotExist(err) {
		t.Fatal("graded failed agent", err)
	}
}

func TestLauncherRejectsInvalidSelectionAndExistingOutput(t *testing.T) {
	for _, args := range [][]string{{"--tier", "easy", "easy-01-probe"}, {"--all", "--tier", "easy"}, {"unknown"}, {"easy-01-probe", "easy-01-probe"}, {"easy-01-probe", "--", "-config", "/unexpected"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			_, script, env := launcher(t)
			output, err := invoke(t, script, env, append([]string{"--no-build"}, args...)...)
			if err == nil {
				t.Fatal(output)
			}
		})
	}
	root, script, env := launcher(t)
	output, err := invoke(t, script, env, "--no-build", "--out", root, "easy-01-probe")
	if err == nil || !strings.Contains(output, "output already exists") {
		t.Fatal(output, err)
	}
}

func TestLauncherTierAndGraderFailure(t *testing.T) {
	root, script, env := launcher(t)
	env = append(env, "FAIL_GRADE=1")
	output, err := invoke(t, script, env, "--no-build", "--out", filepath.Join(root, "batch"), "--tier", "easy")
	if err == nil || !strings.Contains(output, "Grader failed") {
		t.Fatal(output, err)
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls"))
	if strings.Count(string(calls), "-problem\t") != 1 {
		t.Fatal(string(calls))
	}
}
