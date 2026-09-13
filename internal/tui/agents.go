package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

// Tables are immutable snapshots. Async counts replace a table so frozen
// transcript entries keep their original values, including across resizes.
type agentsTable struct {
	id   uint64
	rows [][6]string
}

type agentTableCount struct {
	agent    message.ActorID
	revision uint64
	table    uint64
	row      int
	count    int64
	err      error
}

func (m *model) showAgents() tea.Cmd {
	m.nextTableID++
	table := &agentsTable{id: m.nextTableID}
	var commands []tea.Cmd
	session, canCount := m.session.(tokenSession)
	for _, info := range m.session.Agents() {
		m.ensureStream(info.ID).parent = info.Parent
		name := string(info.ID)
		if info.ID == m.session.Root() {
			name += " (root)"
		}
		row := [6]string{name, string(info.State), string(info.Parent), "unknown", "unknown", "unknown"}
		inspection, err := m.session.InspectAgent(info.ID, conversation.InspectOptions{})
		if err == nil {
			row[1] = string(inspection.State)
			if inspection.StateRevision >= m.revisions[info.ID] {
				m.states[info.ID] = inspection.State
				m.revisions[info.ID] = inspection.StateRevision
			}
			if latest := inspection.Usage.Latest; latest == nil {
				row[4] = "—"
			} else if latest.Usage != nil && latest.Usage.OutputTokens != nil && *latest.Usage.OutputTokens >= 0 {
				row[4] = tokenDigits(*latest.Usage.OutputTokens)
			}
			if limit := inspection.OutputTokenLimit; limit != nil && *limit > 0 {
				row[5] = tokenDigits(*limit)
			}
			if canCount && inspection.ContextRevision > 0 {
				row[3] = "counting…"
				commands = append(commands, countAgentTable(m.ctx, session, table.id, len(table.rows), info.ID, inspection.ContextRevision))
			}
		}
		for i := range row {
			row[i] = strings.NewReplacer("\n", " ", "\t", " ").Replace(safeText(row[i]))
		}
		table.rows = append(table.rows, row)
	}
	m.add("Agents", table.render(0), true)
	m.entries[len(m.entries)-1].agents = table
	m.entries[len(m.entries)-1].renderWidth = 0
	m.renderTranscript(true)
	return tea.Batch(commands...)
}

func countAgentTable(ctx context.Context, session tokenSession, table uint64, row int, id message.ActorID, revision uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		count, err := session.CountAgentTokens(ctx, id, revision)
		return agentTableCount{agent: id, revision: revision, table: table, row: row, count: count, err: err}
	}
}

func (m *model) finishAgentTableCount(result agentTableCount) {
	if result.agent != "" {
		v := m.ensureStream(result.agent)
		if v.context == nil || result.revision >= v.context.revision {
			v.context = &contextTokens{revision: result.revision, count: result.count, failed: result.err != nil || result.count < 0}
		}
	}
	for i := range m.entries {
		e := &m.entries[i]
		if e.agents == nil || e.agents.id != result.table || result.row < 0 || result.row >= len(e.agents.rows) {
			continue
		}
		table := *e.agents
		table.rows = append([][6]string(nil), table.rows...)
		table.rows[result.row][3] = "unknown"
		if result.err == nil && result.count >= 0 {
			table.rows[result.row][3] = tokenDigits(result.count)
		}
		e.agents = &table
		e.body, e.renderWidth = table.render(0), 0
		if !m.selecting {
			m.renderTranscript(false)
		}
		return
	}
}

func (t *agentsTable) render(width int) string {
	if len(t.rows) == 0 {
		return "No agents."
	}
	headers := [6]string{"Agent", "State", "Parent", "Context", "Last output", "Output cap"}
	sizes := [6]int{}
	for i, header := range headers {
		sizes[i] = ansi.StringWidth(header)
		for _, row := range t.rows {
			sizes[i] = max(sizes[i], ansi.StringWidth(row[i]))
		}
	}
	total := 2 * (len(headers) - 1)
	for _, size := range sizes {
		total += size
	}
	var lines []string
	if width <= 0 || total <= width {
		format := func(row [6]string) string {
			cells := make([]string, len(row))
			for i, cell := range row {
				padding := strings.Repeat(" ", sizes[i]-ansi.StringWidth(cell))
				if i >= 3 {
					cells[i] = padding + cell
				} else {
					cells[i] = cell + padding
				}
			}
			return strings.TrimRight(strings.Join(cells, "  "), " ")
		}
		lines = append(lines, format(headers))
		for _, row := range t.rows {
			lines = append(lines, format(row))
		}
	} else {
		// Preserve the values with labels when columns cannot fit the terminal.
		for _, row := range t.rows {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, row[0]+" · "+row[1]+" · parent "+row[2])
			for i := 3; i < len(headers); i++ {
				lines = append(lines, headers[i]+": "+row[i])
			}
		}
	}
	return strings.Join(lines, "\n") + "\n\nTokens · context snapshot at /agents; last output and cap are per call.\n— = no calls yet; unknown = unavailable. Run /agents to refresh."
}
