package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
)

type sendMessageArgs struct {
	To      message.ActorID `json:"to"`
	Message string          `json:"message"`
}

// SendMessage routes an instruction through the executing agent's bound sender.
func SendMessage() Tool {
	return builtin("send_message",
		"Send an instruction to an existing agent. Returns a queued receipt; delivery status and replies are separate.",
		func(ctx context.Context, call Call, args sendMessageArgs) (Result, error) {
			if call.Sender == nil {
				return Result{}, errors.New("send_message requires an agent sender")
			}
			receipt, err := call.Sender.Send(ctx, message.Draft{To: args.To, Kind: message.Instruction, Content: args.Message})
			if err != nil {
				return Result{}, err
			}
			return JSON(receipt)
		})
}

// MessageStatus reads delivery state through the supplied lookup function.
func MessageStatus(lookup func(message.MessageID) (message.Receipt, bool)) Tool {
	type args struct {
		ID message.MessageID `json:"message_id"`
	}
	return builtin("message_status",
		"Read a message's latest receipt: queued, consumed, or undelivered. Consumed does not mean completed.",
		func(ctx context.Context, _ Call, a args) (Result, error) {
			receipt, ok := lookup(a.ID)
			if !ok {
				return Result{}, errors.New("unknown message")
			}
			return JSON(receipt)
		})
}

var creationParameters = parameters[roster.CreateRequest](
	Enum("role", "implementor", "auditor", "researcher"),
	Description("role", "implementor executes tasks and repairs; auditor independently reviews submitted work; researcher investigates a bounded question."),
)

// DecodeAgentCreation validates the same wire contract advertised by CreateAgent.
func DecodeAgentCreation(raw json.RawMessage) (roster.CreateRequest, error) {
	return creationParameters.Decode(raw)
}

// CreateAgent creates an idle registered execution agent; assignment is separate.
func CreateAgent(handle Handler[roster.CreateRequest]) Tool {
	return Func[roster.CreateRequest]{Spec: Definition[roster.CreateRequest]{
		Name: "create_agent", Description: "Create an idle agent. Choose implementor for tasks or repairs, auditor for independent review, or researcher for investigation. Returns agent_id and role. Then call assign_implementation, assign_repair, assign_audit, or assign_research with agent_id as assignee. Creation alone does not start a task.", Parameters: creationParameters,
	}, Invoke: handle}
}

// Management tools decode IDs and invoke application-supplied operations. Their
// handlers return snapshots/acknowledgments without exposing controller types.
func StopAgent(handle func(context.Context, Call, message.ActorID) (Result, error)) Tool {
	return agentOperation("stop_agent", "Request cancellation of an agent. stop_requested is not confirmation of exit; stopped and failed are terminal.", handle)
}
func PauseAgent(handle func(context.Context, Call, message.ActorID) (Result, error)) Tool {
	return agentOperation("pause_agent", "Request a pause at the next operation boundary. An in-flight model or tool call finishes first. pause_requested is not paused.", handle)
}
func ResumeAgent(handle func(context.Context, Call, message.ActorID) (Result, error)) Tool {
	return agentOperation("resume_agent", "Resume the same paused agent or retract its pending pause. Stopped agents cannot resume.", handle)
}

type InspectAgentArgs struct {
	AgentID message.ActorID `json:"agent_id"`
	Limit   *int            `json:"limit"`
	Before  *uint64         `json:"before"`
}

func InspectAgent(handle func(context.Context, Call, InspectAgentArgs) (Result, error)) Tool {
	return builtin("inspect_agent",
		"Read an agent's current lifecycle state and actual conversation transcript. Defaults to the latest 20 messages, maximum 100. Results are chronological. Use the first returned position as before to read earlier messages. This reads a snapshot without messaging or interrupting the target.",
		func(ctx context.Context, call Call, args InspectAgentArgs) (Result, error) {
			if strings.TrimSpace(string(args.AgentID)) == "" {
				return Result{}, errors.New("agent_id must not be empty")
			}
			return handle(ctx, call, args)
		},
		Nullable("limit", "use the default page size"), Nullable("before", "read the latest messages"), MinLength("agent_id", 1), Minimum("limit", 1), Maximum("limit", 100), Minimum("before", 1))
}

func agentOperation(name, description string, handle func(context.Context, Call, message.ActorID) (Result, error)) Tool {
	type args struct {
		AgentID message.ActorID `json:"agent_id"`
	}
	return builtin(name, description,
		func(ctx context.Context, c Call, a args) (Result, error) {
			if strings.TrimSpace(string(a.AgentID)) == "" {
				return Result{}, errors.New("agent_id must not be empty")
			}
			return handle(ctx, c, a.AgentID)
		},
		MinLength("agent_id", 1))
}
func ListAgents(handle func(context.Context, Call) (Result, error)) Tool {
	return builtin("list_agents",
		"List agents and their current lifecycle states.",
		func(ctx context.Context, c Call, _ struct{}) (Result, error) { return handle(ctx, c) })
}
