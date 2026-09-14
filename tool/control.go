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
func WaitForInput() Tool {
	return inboxWait{builtin("wait_for_input", "Wait for new inbox input. Must be the sole tool call. Ends this exchange without a final reply or a work-state change; already queued input is processed next.", func(context.Context, Call, struct{}) (Result, error) {
		return JSON(struct {
			Waiting bool `json:"waiting"`
		}{true})
	})}
}
