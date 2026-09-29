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

// SendMessage routes a message through the executing agent's bound sender.
// resolve turns a role name such as "manager" into an agent id; the host's
// topology decides whether the sender may reach that agent.
func SendMessage(resolve func(message.ActorID) message.ActorID) Tool {
	return builtin("send_message",
		"Send a message to an agent you are connected to. to is an agent id, or the role of an agent you can reach, such as \"manager\". You can reach only the agents the session connects you to; any other recipient is refused with the list you can reach. Your final text reply goes to whoever you work for without this tool. Returns a queued receipt; replies arrive as messages.",
		func(ctx context.Context, call Call, args sendMessageArgs) (Result, error) {
			if call.Sender == nil {
				return Result{}, errors.New("send_message requires an agent sender")
			}
			to := args.To
			if resolve != nil {
				to = resolve(to)
			}
			receipt, err := call.Sender.Send(ctx, message.Draft{To: to, Kind: message.Instruction, Content: args.Message})
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

const workerRoles = "implementor executes tasks and repairs; reviewer reads files on this machine, in the workspace or any folder the user names such as ~/Downloads, and reports what they contain; web_researcher answers a bounded question from web searches and pages; deep_researcher runs a long multi-source deep research investigation, only when the user explicitly asked for deep research; experimenter measures how the code behaves, such as its performance or a bug's cause, by testing hypotheses in its own copy of the workspace."

// creationParameters offers auditors only when the manager assigns audits
// itself; otherwise the harness creates a new auditor for every submission.
func creationParameters(auditors bool) Parameters[roster.CreateRequest] {
	if auditors {
		return parameters[roster.CreateRequest](Enum("role", "implementor", "auditor", "reviewer", "web_researcher", "deep_researcher", "experimenter"),
			Description("role", workerRoles+" auditor independently reviews one submission; every audit needs a new one."))
	}
	return parameters[roster.CreateRequest](Enum("role", "implementor", "reviewer", "web_researcher", "deep_researcher", "experimenter"), Description("role", workerRoles))
}

// DecodeAgentCreation validates a creation request, including auditors.
func DecodeAgentCreation(raw json.RawMessage) (roster.CreateRequest, error) {
	return creationParameters(true).Decode(raw)
}

// CreateAgent creates an idle registered execution agent; assignment is
// separate. auditors offers the auditor role, for owners that assign audits.
func CreateAgent(handle Handler[roster.CreateRequest], auditors bool) Tool {
	description := "Create an idle agent. Choose implementor for tasks or repairs, reviewer to read and report on files anywhere on this machine, web_researcher for external information, deep_researcher only when the user explicitly asked for deep research, or experimenter to measure how the code behaves. Returns agent_id and role. assign_task with a null assignee creates the agent itself, so create_agent is only for an agent you want idle before its work. The harness creates auditors itself. Creation alone does not start a task."
	if auditors {
		description = "Create an idle agent. Choose implementor for tasks or repairs, auditor for an independent review of one submission, reviewer to read and report on files anywhere on this machine, web_researcher for external information, deep_researcher only when the user explicitly asked for deep research, or experimenter to measure how the code behaves. Returns agent_id and role. assign_task with a null assignee creates the agent itself, so create_agent is only for an agent you want idle before its work, or an auditor. Creation alone does not start a task."
	}
	return Func[roster.CreateRequest]{Spec: Definition[roster.CreateRequest]{Name: "create_agent", Description: description, Parameters: creationParameters(auditors)}, Invoke: handle}
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
		"Read an agent's current lifecycle state and actual conversation transcript. Set before to null to read the latest messages. before is a transcript position, not a count: set it only to the first position an earlier inspect_agent result returned, to read the messages before that one. limit is how many messages to return: null for 20, at most 100. Results are chronological. This reads a snapshot without messaging or interrupting the target.",
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
