package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

type reasoningOutput struct {
	Output projection.OutputView
	Text   string
}

type reasoningSource interface {
	readReasoning(context.Context, identity.ActorID, uint64) ([]reasoningOutput, bool, error)
}

// The shared reducer retains output metadata even when /clear clears UI rows.
// Text is fetched explicitly at a fixed cursor; no model-history entry is added.
func (s *observedSession) readReasoning(ctx context.Context, actor identity.ActorID, before uint64) ([]reasoningOutput, bool, error) {
	reader, ok := s.Session.(interface {
		ReadOutputText(context.Context, harness.OutputTextQuery) (harness.TextPage, error)
	})
	if !ok {
		return nil, false, errors.New("reasoning inspection unavailable")
	}
	outputs, earlier, err := s.projection.Outputs(actor, before, 20)
	if err != nil {
		return nil, false, err
	}
	result := make([]reasoningOutput, 0, len(outputs))
	for _, output := range outputs {
		var text strings.Builder
		offset := uint64(0)
		for offset < output.ReasoningBytes {
			page, err := reader.ReadOutputText(ctx, harness.OutputTextQuery{Output: output.ID, Channel: provider.ChannelReasoning, Through: output.Through, Offset: offset, MaxBytes: 64 << 10})
			if err != nil {
				return nil, false, err
			}
			if page.Next <= offset {
				return nil, false, errors.New("reasoning page made no progress")
			}
			text.WriteString(page.Text)
			offset = page.Next
		}
		result = append(result, reasoningOutput{Output: output, Text: text.String()})
	}
	return result, earlier, nil
}

type reasoningLoaded struct {
	view    *transcriptView
	outputs []reasoningOutput
	earlier bool
	before  uint64
	err     error
}

func (m *model) loadReasoning(before uint64) tea.Cmd {
	v := m.transcript
	if v.reasoningLoading {
		return nil
	}
	source, ok := m.session.(reasoningSource)
	if !ok {
		v.err = "Reasoning inspection unavailable"
		return nil
	}
	v.reasoningLoading = true
	v.err = ""
	actor := v.inspection.ID
	m.renderAgentTranscript()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		outputs, earlier, err := source.readReasoning(ctx, actor, before)
		return reasoningLoaded{view: v, outputs: outputs, earlier: earlier, before: before, err: err}
	}
}

func (m *model) finishReasoning(msg reasoningLoaded) {
	if m.transcript != msg.view {
		return
	}
	v := m.transcript
	v.reasoningLoading = false
	if msg.err != nil {
		v.err = msg.err.Error()
		if !v.copying {
			m.renderAgentTranscript()
		}
		return
	}
	oldLines, offset := v.viewport.TotalLineCount(), v.viewport.YOffset
	if msg.before == 0 {
		v.reasoningOutputs = msg.outputs
	} else {
		v.reasoningOutputs = append(msg.outputs, v.reasoningOutputs...)
	}
	v.reasoningEarlier = msg.earlier
	if !v.copying {
		m.renderAgentTranscript()
		if v.reasoning {
			if msg.before == 0 {
				v.viewport.GotoBottom()
			} else {
				v.viewport.SetYOffset(offset + v.viewport.TotalLineCount() - oldLines)
			}
		}
	}
}

func reasoningBody(v *transcriptView) string {
	var b strings.Builder
	b.WriteString("Recorded reasoning · excluded from model context\n\n")
	if v.reasoningLoading {
		b.WriteString("Loading…\n\n")
	}
	for _, r := range v.reasoningOutputs {
		fmt.Fprintf(&b, "Call %d · %s", r.Output.ID.Call, r.Output.Status)
		if r.Output.HistoryPosition != nil {
			fmt.Fprintf(&b, " · history message %d", *r.Output.HistoryPosition)
		}
		b.WriteString("\n")
		if r.Output.Error != nil {
			b.WriteString(r.Output.Error.Message + "\n")
		}
		if r.Text == "" {
			b.WriteString("No reasoning recorded.\n")
		} else {
			b.WriteString(r.Text + "\n")
		}
		b.WriteString("\n")
	}
	if len(v.reasoningOutputs) == 0 && !v.reasoningLoading {
		b.WriteString("No outputs observed yet.\n")
	}
	return b.String()
}
