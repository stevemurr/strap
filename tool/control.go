package tool

import "context"

type ControlKind string

const (
	YieldToInbox ControlKind = "yield_to_inbox"
	// FinishOnSuccess ends the exchange once the call succeeds, after the rest
	// of its batch: a worker's submission is its handoff, and the closing
	// reply it used to write repeated what the work record already says.
	FinishOnSuccess ControlKind = "finish_on_success"
)

type ControlTool interface {
	Tool
	Control() ControlKind
}
type inboxWait struct{ Func[struct{}] }

func (inboxWait) Control() ControlKind     { return YieldToInbox }
func (t inboxWait) snapshot() preparedTool { return t }

// Finishing marks t as ending its agent's exchange when it succeeds.
func Finishing(t Tool) Tool { return finishing{t} }

type finishing struct{ Tool }

func (finishing) Control() ControlKind { return FinishOnSuccess }
func (f finishing) InputContract() Contract {
	if typed, ok := f.Tool.(interface{ InputContract() Contract }); ok {
		return typed.InputContract()
	}
	return Contract{}
}
func (f finishing) Validate() error { return ValidateTool(f.Tool) }
func (f finishing) BookkeepingParameters() []string {
	if b, ok := f.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

// WaitForInput grants explicit runtime control; model text and ordinary results
// cannot cause a yield. The agent enforces the sole-call batch contract.
func WaitForInput() Tool { return WaitForInputWhen(nil) }

// WaitForInputWhen is WaitForInput with a precondition. When allowed returns an
// error the call fails with it and no yield happens, so the model's next turn
// can send the text-only reply it should have sent instead of waiting.
func WaitForInputWhen(allowed func(context.Context, Call) error) Tool {
	description := "Wait for new inbox input. Must be the sole tool call. Ends this exchange without a final reply or a work-state change; already queued input is processed next."
	if allowed != nil {
		description += " Rejected when nothing you are waiting for can arrive; a finished task or a question for the user is a text-only reply, not a wait."
	}
	return inboxWait{builtin("wait_for_input", description, func(ctx context.Context, c Call, _ struct{}) (Result, error) {
		if allowed != nil {
			if err := allowed(ctx, c); err != nil {
				return Result{}, err
			}
		}
		return JSON(struct {
			Waiting bool `json:"waiting"`
		}{true})
	})}
}
