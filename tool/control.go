package tool

import "context"

type ControlKind string

const YieldToInbox ControlKind = "yield_to_inbox"

type ControlTool interface {
	Tool
	Control() ControlKind
}
type inboxWait struct{ Func[struct{}] }

func (inboxWait) Control() ControlKind     { return YieldToInbox }
func (t inboxWait) snapshot() preparedTool { return t }

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
