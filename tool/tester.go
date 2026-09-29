package tool

// ReportFailureArgs names a failing test the adversarial tester wrote.
type ReportFailureArgs struct {
	Path        string `json:"path"`
	Command     string `json:"command"`
	Requirement string `json:"requirement"`
}

// ReportFailure is the adversarial tester's one way to report: the harness
// runs command itself, and only a command that fails reaches the agent.
func ReportFailure(h Handler[ReportFailureArgs]) Tool {
	return builtin("report_failure", "Report a test that fails against the change: path is your test file, command runs just that test and must fail now, and requirement quotes the sentence of the request or document the test checks. The harness runs command itself; a command that passes is not reported.", h,
		MinLength("path", 1), MinLength("command", 1), MinLength("requirement", 1))
}
