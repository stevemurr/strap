// Package tool defines an operation a model can request.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// Call separates model-generated arguments from runtime-supplied identity and routing.
// Actor and Sender belong to the executing agent; they are not model inputs.
type Call struct {
	InvocationID string // Host-generated invocation, never a model argument.
	Arguments    json.RawMessage
	Actor        message.ActorID
	Sender       message.Sender
}

// Tool implementations must honor cancellation. Errors become model-visible
// results. A tool may be shared by agents, so its implementation must allow
// concurrent calls or provide its own synchronization.
type Tool interface {
	Definition() provider.ToolDefinition
	Call(context.Context, Call) (Result, error)
}

// Definition connects a model-facing definition to its argument contract.
type Definition[A any] struct {
	Name        string
	Description string
	Parameters  Parameters[A]
	// Bookkeeping names fields inside input that the model copies from a previous
	// receipt, such as revisions. They carry no intent, so the agent ignores
	// them when deciding whether a call repeats the previous one.
	Bookkeeping []string
}

func (d Definition[A]) ProviderDefinition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: d.Name, Description: d.Description, Parameters: d.Parameters.Schema()}
}

// Handler receives validated arguments; Call supplies runtime identity and routing.
type Handler[A any] func(context.Context, Call, A) (Result, error)

// Func implements the runtime Tool interface with a typed argument contract.
// Construct Parameters once before sharing the tool between agents.
type Func[A any] struct {
	Spec   Definition[A]
	Invoke Handler[A]
}

func (f Func[A]) Definition() provider.ToolDefinition { return f.Spec.ProviderDefinition() }

// Bookkeeping returns a copy of the tool that declares the named parameters as
// bookkeeping; see Definition.Bookkeeping.
func (f Func[A]) Bookkeeping(names ...string) Func[A] {
	f.Spec.Bookkeeping = append(slices.Clone(f.Spec.Bookkeeping), names...)
	return f
}

// BookkeepingParameters is read by the agent at registration.
func (f Func[A]) BookkeepingParameters() []string { return slices.Clone(f.Spec.Bookkeeping) }

// builtin declares a statically-declared tool from its model-facing name,
// description, handler and argument constraints. An invalid constraint panics at
// initialization, exactly as parameters does. It returns Func[A] rather than Tool
// because Compose type-asserts its branches to preparedTool. A Compose branch
// passes an empty description; compose emits one only when non-empty.
func builtin[A any](name, description string, invoke Handler[A], constraints ...Constraint) Func[A] {
	return Func[A]{
		Spec:   Definition[A]{Name: name, Description: description, Parameters: parameters[A](constraints...)},
		Invoke: invoke,
	}
}

// Validate is called by the agent at registration, before advertising the tool.
func (f Func[A]) Validate() error {
	if f.Spec.Name == "" {
		return errors.New("tool has no name")
	}
	if f.Invoke == nil {
		return errors.New("tool has no handler")
	}
	if f.Spec.Parameters.root == nil {
		return errors.New("tool has uninitialized parameters")
	}
	return nil
}

func (f Func[A]) prepare(ctx context.Context, call Call) (func() (Result, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	args, err := f.Spec.Parameters.Decode(call.Arguments)
	if err != nil {
		return nil, err
	}
	return func() (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return f.Invoke(ctx, call, args)
	}, nil
}

func (f Func[A]) Call(ctx context.Context, call Call) (Result, error) {
	invoke, err := f.prepare(ctx, call)
	if err != nil {
		return Result{}, err
	}
	return invoke()
}

// Result carries ordered text and images into the calling agent's model history.
type ExecutionBinding struct {
	EvidenceRef        string           `json:"evidence_ref"`
	WorkID             string           `json:"work_id"`
	AssignedAtRevision uint64           `json:"assigned_at_revision"`
	Actor              identity.ActorID `json:"actor"`
}
type Result struct {
	Captured  content.Content `json:"captured,omitempty"` // Complete retained host capture, not model history.
	Content   content.Content
	Execution *ExecutionBinding `json:"execution,omitempty"` // Host attribution; not model content.
}

func Text(text string) Result { return Result{Content: content.Text(text)} }

// JSON returns a structured value as a text content part.
func JSON(value any) (Result, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Result{}, err
	}
	return Text(string(encoded)), nil
}

func (f Func[A]) snapshot() preparedTool { return f }

func (f Func[A]) contract() *parameterNode { return f.Spec.Parameters.root }
