package harness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/tool"
)

// Inspection is a bounded model-facing projection. The controller snapshot and
// canonical thread remain unchanged. Envelope attribution is already in Content.
func inspectTool(c agentControl) tool.Tool {
	return tool.InspectAgent(func(ctx context.Context, _ tool.Call, args tool.InspectAgentArgs) (tool.Result, error) {
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		q := agent.TranscriptQuery{}
		if args.Limit != nil {
			q.Limit = *args.Limit
		}
		if args.Before != nil {
			q.Before = *args.Before
		}
		inspection, err := c.InspectAgent(args.AgentID, conversation.InspectOptions{Transcript: &q})
		if err != nil {
			return tool.Result{}, err
		}
		return inspectionResult(inspection)
	})
}

type inspectionEntry struct {
	Position uint64 `json:"position"`
	Role     string `json:"role"`
	// Text preserves text and raw tool arguments as display content, not executable JSON.
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type inspectionPage struct {
	Entries    []inspectionEntry `json:"entries"`
	HasEarlier bool              `json:"has_earlier"`
}

func inspectionResult(in AgentInspection) (tool.Result, error) {
	out := struct {
		AgentInfo
		Transcript inspectionPage `json:"transcript"`
	}{AgentInfo: in.Info(), Transcript: inspectionPage{Entries: []inspectionEntry{}, HasEarlier: in.Transcript.HasEarlier}}
	// Encoded entry budget accounts for JSON escaping, not only source text size.
	budget := 32 * 1024
	for i := len(in.Transcript.Entries) - 1; i >= 0; i-- {
		source := in.Transcript.Entries[i]
		m := source.Message
		entry := inspectionEntry{Position: source.Position, Role: m.Role}
		var b strings.Builder
		remaining := 4096
		add := func(s string) {
			if len(s) > remaining {
				s = s[:remaining]
				for !utf8.ValidString(s) {
					s = s[:len(s)-1]
				}
				entry.Truncated = true
			}
			b.WriteString(s)
			remaining -= len(s)
		}
		if m.ToolCallID != "" {
			add("Tool result for " + m.ToolCallID + "\n")
		}
		for _, part := range m.Content {
			if part.Image != nil {
				add("[image: " + part.Image.MIMEType + "; binary data omitted]\n")
			} else {
				add(part.Text)
			}
		}
		for _, call := range m.ToolCalls {
			add("\nTool call " + call.ID + ": " + call.Name + "\n")
			add(string(call.Arguments))
		}
		entry.Text = b.String()
		encoded, err := json.Marshal(entry)
		if err != nil {
			return tool.Result{}, err
		}
		if len(encoded) > budget {
			out.Transcript.HasEarlier = true
			break
		}
		budget -= len(encoded)
		out.Transcript.Entries = append(out.Transcript.Entries, entry)
	}
	slices.Reverse(out.Transcript.Entries)
	return tool.JSON(out)
}
