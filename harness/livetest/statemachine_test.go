package livetest_test

import (
	"os"
	"strings"
)

// assertMachine checks the scenario against the machine (see harness/machine),
// writes the paths each agent and work item took to machine.txt, and fails on
// any violation.
func (l *live) assertMachine() {
	l.t.Helper()
	l.m.mu.Lock()
	tl := l.m.Snapshot()
	l.m.mu.Unlock()
	paths := tl.Paths()
	violations := tl.Violations(true)
	report := paths
	if len(violations) > 0 {
		report += "\nviolations:\n  " + strings.Join(violations, "\n  ") + "\n"
	}
	_ = os.WriteFile(l.out+"/machine.txt", []byte(report), 0o644)
	l.t.Logf("machine paths:\n%s", paths)
	for _, v := range violations {
		l.t.Error(v)
	}
}
