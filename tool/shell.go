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

	"github.com/stevemurr/strap/provider"
)

type ShellConfig struct {
	Dir string
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
	config ShellConfig
	bound  Func[shellArgs]
}

var _ Tool = (*Shell)(nil)

type shellArgs struct {
	Command   string `json:"command"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
}

type ShellResult struct {
	Output    string `json:"output"`
	ExitCode  *int   `json:"exit_code"` // nil when terminated without a normal exit
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
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
	s := &Shell{config: config}
	params, err := NewParameters[shellArgs](MinLength("command", 1), Minimum("timeout_ms", 1), Maximum("timeout_ms", config.MaxTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	s.bound = Func[shellArgs]{
		Spec: Definition[shellArgs]{
			Name:        "shell",
			Description: s.description(),
			Parameters:  params,
		},
		Invoke: s.handle,
	}
	return s, nil
}

func (s *Shell) Definition() provider.ToolDefinition { return s.bound.Definition() }
func (s *Shell) Validate() error                     { return s.bound.Validate() }
func (s *Shell) description() string {
	return fmt.Sprintf("Run a synchronous shell command in %s using %s. Returns combined stdout/stderr and exit status. Default timeout %d ms, maximum %d ms. Output is bounded, keeping both ends. No persistent shell or background jobs; descendants in the process group are stopped when the call ends. Runs with host permissions, without a sandbox.", s.config.Dir, s.config.Program, s.config.Timeout.Milliseconds(), s.config.MaxTimeout.Milliseconds())
}
func (s *Shell) Call(ctx context.Context, call Call) (Result, error) { return s.bound.Call(ctx, call) }

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
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start shell: %w", err)
	}
	defer stopProcessGroup(cmd)
	err := cmd.Wait()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	result := ShellResult{
		Output: output.String(), Truncated: output.truncated,
		TimedOut:         errors.Is(runCtx.Err(), context.DeadlineExceeded),
		OutputIncomplete: errors.Is(err, exec.ErrWaitDelay),
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) && !result.TimedOut && !result.OutputIncomplete {
		return Result{}, fmt.Errorf("wait for shell: %w", err)
	}
	return JSON(result)
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

func (s *Shell) prepare(ctx context.Context, call Call) (func() (Result, error), error) {
	return s.bound.prepare(ctx, call)
}
func (s *Shell) snapshot() preparedTool { return s.bound.snapshot() }
