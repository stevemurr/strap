// Package evalwire carries resolved configuration and progress across the eval
// container boundary. It does not select a workspace or execute a task.
package evalwire

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
)

const Version = 1

type Config struct {
	Version int            `json:"version"`
	Harness harness.Config `json:"harness"`
	Profile string         `json:"profile"`
}

func ParseConfig(b []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Version != Version {
		return c, fmt.Errorf("unsupported eval configuration version %d", c.Version)
	}
	for _, model := range []*harness.ModelConfig{&c.Harness.Model, c.Harness.Root.Model, c.Harness.Implementor.Model, c.Harness.Auditor.Model, c.Harness.Researcher.Model} {
		if model == nil {
			continue
		}
		if model.Timeout <= 0 {
			return c, fmt.Errorf("model timeout must be positive")
		}
		if _, err := model.Resolve(); err != nil {
			return c, err
		}
	}
	return c, nil
}

type Progress struct {
	Version int             `json:"version"`
	Task    string          `json:"task"`
	Phase   eval.Phase      `json:"phase,omitempty"`
	At      time.Time       `json:"at"`
	Event   *eventlog.Event `json:"event,omitempty"`
}

// FromProgress excludes output deltas and full histories; the durable trace
// remains authoritative. Only the events used by the live page cross stdout.
func FromProgress(p eval.Progress) (Progress, bool, error) {
	r := Progress{Version: Version, Task: p.Task.ID, Phase: p.Phase, At: p.At}
	if p.Result != nil {
		return r, false, nil
	}
	if p.Event == nil {
		return r, true, nil
	}
	switch p.Event.(type) {
	case conversation.ToolEvent, conversation.MessageEvent, conversation.CommentaryEvent,
		conversation.AgentStarted, conversation.AgentExited, conversation.AgentStateChanged,
		conversation.UsageEvent, conversation.ContextTokensEvent, conversation.DiagnosticEvent, conversation.WorkEvent:
	default:
		return r, false, nil
	}
	d, err := eventcodec.EncodeEvent(p.Event)
	if err != nil {
		return r, false, err
	}
	r.Event = &eventlog.Event{Schema: eventlog.SchemaVersion, Data: d}
	return r, true, nil
}

func (p Progress) Decode(task eval.Task) (eval.Progress, error) {
	r := eval.Progress{Task: task, Phase: p.Phase, At: p.At}
	if p.Version != Version || p.Task != task.ID {
		return r, fmt.Errorf("unexpected eval progress version/task: %d/%q", p.Version, p.Task)
	}
	if p.Event != nil {
		e, err := eventcodec.DecodeEvent(*p.Event)
		if err != nil {
			return r, err
		}
		r.Event = e
	}
	return r, nil
}
