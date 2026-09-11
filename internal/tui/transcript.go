package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

// This view holds only independent snapshots. The main viewport, draft, and event
// reader keep their existing ownership while the user browses another thread.
type transcriptView struct {
	inspection conversation.AgentInspection
	viewport   viewport.Model
	raw        bool
	err        string
	mainOffset int
}

func (m *model) openTranscript(id message.ActorID) {
	in, err := m.session.InspectAgent(id, conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil {
		if m.transcript != nil {
			m.transcript.err = err.Error()
		} else {
			m.add("Error", err.Error(), true)
		}
		return
	}
	raw := m.transcript != nil && m.transcript.raw
	mainOffset := m.viewport.YOffset
	if m.transcript != nil {
		mainOffset = m.transcript.mainOffset
	}
	m.transcript = &transcriptView{inspection: in, viewport: viewport.New(max(1, m.width), max(1, m.height-4)), raw: raw, mainOffset: mainOffset}
	m.resizeAgentTranscript()
	m.transcript.viewport.GotoBottom()
}

func (m *model) resizeAgentTranscript() {
	v := m.transcript
	v.viewport.Width = max(1, m.width)
	v.viewport.Height = max(1, m.height-4)
	m.renderAgentTranscript()
}

func (m *model) renderAgentTranscript() {
	v := m.transcript
	var b strings.Builder
	if v.inspection.Transcript == nil {
		v.viewport.SetContent("No transcript returned.")
		return
	}
	for _, e := range v.inspection.Transcript.Entries {
		fmt.Fprintf(&b, "%d  %s\n", e.Position, e.Message.Role)
		if v.raw {
			// Raw JSON includes the stored message and envelope. Binary images are shown
			// as metadata in this terminal view; inspection itself retains their bytes.
			m := e.Message
			type imageInfo struct {
				MIMEType string
				Bytes    int
			}
			type part struct {
				Text  string
				Image *imageInfo `json:",omitempty"`
			}
			parts := make([]part, len(m.Content))
			for i, p := range m.Content {
				parts[i].Text = p.Text
				if p.Image != nil {
					parts[i].Image = &imageInfo{p.Image.MIMEType, len(p.Image.Data)}
				}
			}
			type callView struct {
				ID        string
				Name      string
				Arguments string
			}
			calls := make([]callView, len(m.ToolCalls))
			for i, c := range m.ToolCalls {
				calls[i] = callView{c.ID, c.Name, string(c.Arguments)}
			}
			raw, _ := json.MarshalIndent(struct {
				Role       string
				Content    []part
				Envelope   *message.Message
				ToolCalls  []callView
				ToolCallID string
			}{m.Role, parts, m.Envelope, calls, m.ToolCallID}, "", "  ")
			b.WriteString(string(raw))
			b.WriteString("\n")
		} else {
			m := e.Message
			if m.Envelope != nil {
				env := m.Envelope
				fmt.Fprintf(&b, "%s → %s · %s · %s\n", env.From, env.To, env.Kind, env.ID)
				if env.Content != "" {
					b.WriteString(env.Content + "\n")
				}
				if env.Work != nil {
					raw, _ := json.MarshalIndent(env.Work, "", "  ")
					b.Write(raw)
					b.WriteString("\n")
				}
				if env.Event != nil {
					raw, _ := json.MarshalIndent(env.Event, "", "  ")
					b.Write(raw)
					b.WriteString("\n")
				}
			} else {
				for _, p := range m.Content {
					if p.Image != nil {
						fmt.Fprintf(&b, "[image: %s, %d bytes; binary data omitted]\n", p.Image.MIMEType, len(p.Image.Data))
					} else {
						b.WriteString(p.Text + "\n")
					}
				}
			}
			if m.ToolCallID != "" {
				fmt.Fprintf(&b, "Tool result for %s\n", m.ToolCallID)
			}
			for _, call := range m.ToolCalls {
				fmt.Fprintf(&b, "Tool call %s · %s\n%s\n", call.ID, call.Name, call.Arguments)
			}
		}
		b.WriteString("\n")
	}
	if len(v.inspection.Transcript.Entries) == 0 {
		b.WriteString("No messages in this page.\n")
	}
	width := max(1, v.viewport.Width-1)
	lines := strings.Split(ansi.Hardwrap(safeText(b.String()), width, true), "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	v.viewport.SetContent(strings.Join(lines, "\n"))
}

func (m *model) earlierTranscript() {
	v := m.transcript
	page := v.inspection.Transcript
	if page == nil || !page.HasEarlier || len(page.Entries) == 0 {
		return
	}
	in, err := m.session.InspectAgent(v.inspection.ID, conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Before: page.Entries[0].Position}})
	if err != nil {
		v.err = err.Error()
		return
	}
	if in.Transcript == nil {
		v.err = "No transcript returned."
		return
	}
	oldLines, offset := v.viewport.TotalLineCount(), v.viewport.YOffset
	in.Transcript.Entries = append(in.Transcript.Entries, page.Entries...)
	v.inspection = in
	v.err = ""
	m.renderAgentTranscript()
	v.viewport.SetYOffset(offset + v.viewport.TotalLineCount() - oldLines)
}

func (m *model) transcriptKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.transcript
	switch key.String() {
	case "ctrl+c", "ctrl+d":
		return m.quit()
	case "esc":
		m.viewport.SetYOffset(v.mainOffset)
		m.transcript = nil
		return m, nil
	case "r":
		m.openTranscript(v.inspection.ID)
	case "v":
		v.raw = !v.raw
		m.renderAgentTranscript()
	case "[", "]":
		agents := m.session.Agents()
		for i, a := range agents {
			if a.ID == v.inspection.ID {
				delta := 1
				if key.String() == "[" {
					delta = -1
				}
				m.openTranscript(agents[(i+delta+len(agents))%len(agents)].ID)
				break
			}
		}
	case "pgup", "up":
		if v.viewport.AtTop() {
			m.earlierTranscript()
		}
		var cmd tea.Cmd
		v.viewport, cmd = v.viewport.Update(key)
		return m, cmd
	case "pgdown", "down", "ctrl+home", "ctrl+end":
		if key.String() == "ctrl+home" {
			v.viewport.GotoTop()
		} else if key.String() == "ctrl+end" {
			v.viewport.GotoBottom()
		} else {
			var cmd tea.Cmd
			v.viewport, cmd = v.viewport.Update(key)
			return m, cmd
		}
	}
	return m, nil
}

func (m *model) transcriptDisplay() string {
	v := m.transcript
	mode := "formatted"
	if v.raw {
		mode = "raw fields; arguments as text, images as metadata"
	}
	title := fmt.Sprintf("Transcript · %s · %s · parent %s · %s", v.inspection.ID, v.inspection.State, v.inspection.Parent, mode)
	subtitle := "Snapshot · r refresh"
	if p := v.inspection.Transcript; p != nil && len(p.Entries) > 0 {
		subtitle = fmt.Sprintf("Messages %d–%d · snapshot", p.Entries[0].Position, p.Entries[len(p.Entries)-1].Position)
		if p.HasEarlier {
			subtitle += " · scroll above the top for older messages"
		}
	}
	if v.err != "" {
		subtitle = "Error: " + v.err
	}
	line := func(s string) string { return ansi.Truncate(safeText(s), max(1, m.width), "") }
	if m.height < 4 {
		return line(title)
	}
	return strings.Join([]string{lipgloss.NewStyle().Bold(true).Render(line(title)), line(subtitle), v.viewport.View(), line("PgUp/PgDn scroll · [/] agent · r refresh · v raw/formatted · Esc back")}, "\n")
}
