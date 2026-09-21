package tool

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ShellConfig struct {
	// AfterRun runs after waiting for a started command and attempting cleanup.
	// It must return promptly and support concurrent calls.
	AfterRun func()
	Dir      string
	// Program defaults to bash, falling back to /bin/sh.
	Program string
	// Nil selects PATH, HOME, TMPDIR, LANG, LC_ALL and TERM from the host.
	// An explicitly empty slice gives the command an empty environment.
	Env []string
	// Defaults: 30 seconds per call, at most 5 minutes, 64 KiB of output.
	Timeout     time.Duration
	MaxTimeout  time.Duration
	OutputLimit int
}

// Shell is immutable after construction and can be shared by agents. Commands
// run with host permissions: Dir is a working directory, not a sandbox.
type Shell struct {
	stop   func(*exec.Cmd) error
	config ShellConfig
	Func[shellArgs]
}

var _ Tool = (*Shell)(nil)

type shellArgs struct {
	Command   string `json:"command"`
	TimeoutMS *int64 `json:"timeout_ms"`
}

type ShellResult struct {
	Started      bool   `json:"started"`
	Cancelled    bool   `json:"cancelled"`
	OutputLimit  int    `json:"output_limit"`
	StartError   string `json:"start_error,omitempty"`
	WaitError    string `json:"wait_error,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
	Output       string `json:"output"`
	ExitCode     *int   `json:"exit_code"` // nil when terminated without a normal exit
	TimedOut     bool   `json:"timed_out"`
	Truncated    bool   `json:"truncated"`
	// A descendant kept an output pipe open beyond the drain deadline.
	OutputIncomplete bool `json:"output_incomplete,omitempty"`
}

func NewShell(config ShellConfig) (*Shell, error) {
	if !processGroupsSupported {
		return nil, errors.New("shell tools require macOS or Linux")
	}
	var err error
	config.Dir, err = directory(config.Dir)
	if err != nil {
		return nil, err
	}
	if config.Program == "" {
		config.Program, err = exec.LookPath("bash")
		if err != nil {
			config.Program = "/bin/sh"
		}
	}
	config.Program, err = exec.LookPath(config.Program)
	if err != nil {
		return nil, err
	}
	config.Program, err = filepath.Abs(config.Program)
	if err != nil {
		return nil, err
	}
	config.Timeout = cmp.Or(config.Timeout, 30*time.Second)
	config.MaxTimeout = cmp.Or(config.MaxTimeout, 5*time.Minute)
	config.OutputLimit = cmp.Or(config.OutputLimit, 64*1024)
	if config.Timeout < time.Millisecond || config.MaxTimeout < config.Timeout || config.OutputLimit < 2 {
		return nil, errors.New("invalid shell timeout or output limit")
	}
	if config.Env == nil {
		config.Env = []string{}
		for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TERM"} {
			if value, ok := os.LookupEnv(key); ok {
				config.Env = append(config.Env, key+"="+value)
			}
		}
	} else {
		config.Env = append([]string{}, config.Env...)
	}
	s := &Shell{config: config, stop: stopProcessGroup}
	// The researcher's diagnostic shell binds its run to an assignment, so
	// models reach for the same fields here, where nothing records execution.
	params, err := NewParameters[shellArgs](Nullable("timeout_ms", "use the configured timeout"), MinLength("command", 1), Minimum("timeout_ms", 1), Maximum("timeout_ms", config.MaxTimeout.Milliseconds()),
		Reject("", "work_id", "this shell is not bound to work; it takes command and nullable timeout_ms"),
		Reject("", "assigned_at_revision", "this shell is not bound to work; it takes command and nullable timeout_ms"),
		Reject("", "timeout", "the field is timeout_ms, in milliseconds"))
	if err != nil {
		return nil, err
	}
	s.Func = Func[shellArgs]{
		Spec: Definition[shellArgs]{
			Name:        "shell",
			Description: s.description(),
			Parameters:  params,
		},
		Invoke: s.handle,
	}
	return s, nil
}

func (s *Shell) description() string {
	return fmt.Sprintf("Run a synchronous shell command in %s using %s. Returns combined stdout/stderr and exit status. Default timeout %d ms, maximum %d ms. Output is bounded, keeping both ends. No persistent shell or background jobs; descendants in the process group are stopped when the call ends. Runs with host permissions, without a sandbox.", s.config.Dir, s.config.Program, s.config.Timeout.Milliseconds(), s.config.MaxTimeout.Milliseconds())
}
func (s *Shell) handle(ctx context.Context, _ Call, args shellArgs) (Result, error) {
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, errors.New("command must not be empty")
	}
	timeout := s.config.Timeout
	if args.TimeoutMS != nil {
		timeout = time.Duration(*args.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.config.Program, "-c", args.Command)
	cmd.Dir, cmd.Env = s.config.Dir, s.config.Env
	output := newBoundedOutput(s.config.OutputLimit)
	cmd.Stdout, cmd.Stderr = output, output
	// Bound draining even when a descendant inherits a pipe and the shell exits.
	cmd.WaitDelay = 250 * time.Millisecond

	configureProcessGroup(cmd)
	var cancelCleanup error
	cmd.Cancel = func() error { cancelCleanup = s.stop(cmd); return cancelCleanup }
	result := ShellResult{OutputLimit: s.config.OutputLimit}
	if startErr := cmd.Start(); startErr != nil {
		result.StartError = startErr.Error()
		result.Cancelled = ctx.Err() != nil
		result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		encoded, encodeErr := JSON(result)
		return encoded, errors.Join(fmt.Errorf("start shell: %w", startErr), encodeErr)
	}
	if s.config.AfterRun != nil {
		defer s.config.AfterRun()
	}
	result.Started = true
	waitErr := cmd.Wait()
	cleanupErr := s.stop(cmd)
	if errors.Is(cleanupErr, os.ErrProcessDone) {
		cleanupErr = nil
	}
	if errors.Is(cancelCleanup, os.ErrProcessDone) {
		cancelCleanup = nil
	}
	cleanupErr = errors.Join(cancelCleanup, cleanupErr)
	result.Output = output.String()
	result.Truncated = output.truncated
	result.Cancelled = ctx.Err() != nil
	result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
	result.OutputIncomplete = errors.Is(waitErr, exec.ErrWaitDelay)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
	}
	if cleanupErr != nil {
		result.CleanupError = cleanupErr.Error()
	}
	var exit *exec.ExitError
	var unexpected error
	if waitErr != nil && !errors.As(waitErr, &exit) && !result.TimedOut && !result.OutputIncomplete && !result.Cancelled {
		result.WaitError = waitErr.Error()
		unexpected = fmt.Errorf("wait for shell: %w", waitErr)
	}
	encoded, encodeErr := JSON(result)
	return encoded, errors.Join(ctx.Err(), unexpected, cleanupErr, encodeErr)

}

// os/exec serializes writes when stdout and stderr share this writer. Always
// consume all bytes while retaining only the first and last halves of the cap.
type boundedOutput struct {
	head, tail []byte
	limit      int
	truncated  bool
}

func newBoundedOutput(limit int) *boundedOutput { return &boundedOutput{limit: limit} }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	headLimit := b.limit/2 + b.limit%2
	if len(b.head) < headLimit {
		keep := min(len(p), headLimit-len(b.head))
		b.head = append(b.head, p[:keep]...)
		p = p[keep:]
	}
	tailLimit := b.limit - headLimit
	if len(b.tail)+len(p) > tailLimit {
		b.truncated = true
		if len(p) >= tailLimit {
			b.tail = append(b.tail[:0], p[len(p)-tailLimit:]...)
		} else {
			drop := len(b.tail) + len(p) - tailLimit
			copy(b.tail, b.tail[drop:])
			b.tail = append(b.tail[:len(b.tail)-drop], p...)
		}
	} else {
		b.tail = append(b.tail, p...)
	}
	return n, nil
}

func (b *boundedOutput) String() string {
	if b.truncated {
		return strings.ToValidUTF8(string(b.head), "�") + "\n[output truncated]\n" + strings.ToValidUTF8(string(b.tail), "�")
	}
	return strings.ToValidUTF8(string(b.head)+string(b.tail), "�")
}
