package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Messages ---

type schemasLoadedMsg struct {
	schemas map[string][]string // schema -> tables
	err     error
}

type tableSelectedMsg struct {
	schema string
	table  string
}

// --- Tree model ---

type treeEntry struct {
	schema string
	table  string // empty for schema-level entry
}

func (e treeEntry) isSchema() bool { return e.table == "" }
func (e treeEntry) display() string {
	if e.isSchema() {
		return e.schema
	}
	return "  " + e.table
}

// TreeModel is the schema/table sidebar with fuzzy filtering.
type TreeModel struct {
	db *DB

	// All entries in display order (schemas + their tables).
	allEntries []treeEntry
	// Filtered entries currently visible.
	filtered []treeEntry
	cursor   int

	// Schema expansion state.
	expanded map[string]bool

	// Filter input at top.
	filter    textinput.Model
	filtering bool

	// Layout
	width   int
	height  int
	focused bool
}

func NewTreeModel(db *DB) TreeModel {
	ti := textinput.New()
	ti.Placeholder = "filter..."
	ti.CharLimit = 64

	return TreeModel{
		db:       db,
		expanded: make(map[string]bool),
		filter:   ti,
		focused:  true,
	}
}

func (m TreeModel) Init() tea.Cmd {
	return m.loadSchemas()
}

func (m TreeModel) loadSchemas() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		schemas, err := m.db.Schemas(ctx)
		if err != nil {
			return schemasLoadedMsg{err: err}
		}
		result := make(map[string][]string, len(schemas))
		for _, s := range schemas {
			tables, err := m.db.Tables(ctx, s)
			if err != nil {
				return schemasLoadedMsg{err: err}
			}
			result[s] = tables
		}
		return schemasLoadedMsg{schemas: result}
	}
}

func (m TreeModel) Update(msg tea.Msg) (TreeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case schemasLoadedMsg:
		if msg.err != nil {
			slog.Error("failed to load schemas", "error", msg.err)
			return m, nil
		}
		m.allEntries = nil
		for schema, tables := range msg.schemas {
			m.allEntries = append(m.allEntries, treeEntry{schema: schema})
			for _, t := range tables {
				m.allEntries = append(m.allEntries, treeEntry{schema: schema, table: t})
			}
		}
		// Sort: schemas alphabetically, tables under their schema.
		m.sortEntries()
		m.applyFilter()
		return m, nil

	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m TreeModel) handleFilterKey(msg tea.KeyMsg) (TreeModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.filter.SetValue("")
		m.filter.Blur()
		m.applyFilter()
		return m, nil
	case "enter":
		m.filtering = false
		m.filter.Blur()
		if len(m.filtered) == 1 && !m.filtered[0].isSchema() {
			e := m.filtered[0]
			return m, selectTable(e.schema, e.table)
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
		m.cursor = 0
		return m, cmd
	}
}

func (m TreeModel) handleKey(msg tea.KeyMsg) (TreeModel, tea.Cmd) {
	if m.filtering {
		return m.handleFilterKey(msg)
	}

	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter", "l", "right":
		if cmd := m.openEntry(); cmd != nil {
			return m, cmd
		}
	case "h", "left":
		m.collapseEntry()
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "G":
		m.cursor = len(m.filtered) - 1
	case "g":
		m.cursor = 0
	}
	return m, nil
}

func (m *TreeModel) openEntry() tea.Cmd {
	if m.cursor >= len(m.filtered) {
		return nil
	}
	e := m.filtered[m.cursor]
	if e.isSchema() {
		m.expanded[e.schema] = !m.expanded[e.schema]
		m.applyFilter()
		return nil
	}
	return selectTable(e.schema, e.table)
}

func (m *TreeModel) collapseEntry() {
	if m.cursor >= len(m.filtered) {
		return
	}
	e := m.filtered[m.cursor]
	if e.isSchema() {
		m.expanded[e.schema] = false
		m.applyFilter()
		return
	}
	// Jump to parent schema.
	for i := m.cursor - 1; i >= 0; i-- {
		if m.filtered[i].isSchema() && m.filtered[i].schema == e.schema {
			m.cursor = i
			break
		}
	}
}

func selectTable(schema, table string) tea.Cmd {
	return func() tea.Msg {
		return tableSelectedMsg{schema: schema, table: table}
	}
}

func (m *TreeModel) sortEntries() {
	// Group by schema, then sort schemas alphabetically.
	schemaMap := make(map[string][]treeEntry)
	for _, e := range m.allEntries {
		schemaMap[e.schema] = append(schemaMap[e.schema], e)
	}

	schemas := make([]string, 0, len(schemaMap))
	for s := range schemaMap {
		schemas = append(schemas, s)
	}
	slices.Sort(schemas)

	m.allEntries = nil
	for _, s := range schemas {
		m.allEntries = append(m.allEntries, schemaMap[s]...)
	}
}

func (m *TreeModel) applyFilter() {
	query := strings.ToLower(m.filter.Value())
	m.filtered = nil

	if query == "" {
		// No filter: show schemas, and tables under expanded schemas.
		for _, e := range m.allEntries {
			if e.isSchema() {
				m.filtered = append(m.filtered, e)
			} else if m.expanded[e.schema] {
				m.filtered = append(m.filtered, e)
			}
		}
	} else {
		m.applyTableFilter(query)
	}

	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m *TreeModel) applyTableFilter(query string) {
	// Filter matches table names only (substring). Schemas appear
	// as headers when at least one of their tables matches.
	curSchema := ""
	schemaAdded := false
	for _, e := range m.allEntries {
		if e.isSchema() {
			curSchema = e.schema
			schemaAdded = false
			continue
		}
		if !strings.Contains(strings.ToLower(e.table), query) {
			continue
		}
		if !schemaAdded {
			m.filtered = append(m.filtered, treeEntry{schema: curSchema})
			schemaAdded = true
		}
		m.filtered = append(m.filtered, e)
	}
}

// --- View ---

var (
	treeSchemaStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	treeTableStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	treeCursorStyle = lipgloss.NewStyle().Background(lipgloss.Color("8")).Foreground(lipgloss.Color("15"))
	treeTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Padding(0, 1)
	treeBorderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
	treeFocusBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("12"))
)

func (m TreeModel) View() string {
	var b strings.Builder

	// Title.
	b.WriteString(treeTitleStyle.Render("Tables"))
	b.WriteString("\n")

	// Filter bar.
	if m.filtering {
		b.WriteString(m.filter.View())
	} else if m.filter.Value() != "" {
		fmt.Fprintf(&b, " filter: %s", m.filter.Value())
	}
	b.WriteString("\n")

	// Scrollable list area.
	listHeight := m.height - 5 // title + filter + border
	if listHeight < 1 {
		listHeight = 1
	}

	// Determine scroll window.
	start := 0
	if m.cursor >= listHeight {
		start = m.cursor - listHeight + 1
	}
	end := start + listHeight
	if end > len(m.filtered) {
		end = len(m.filtered)
	}

	for i := start; i < end; i++ {
		e := m.filtered[i]
		line := e.display()

		// Pad to width.
		contentWidth := m.width - 4 // border padding
		if contentWidth < 10 {
			contentWidth = 10
		}
		if len(line) < contentWidth {
			line += strings.Repeat(" ", contentWidth-len(line))
		} else if len(line) > contentWidth {
			line = line[:contentWidth]
		}

		if i == m.cursor {
			line = treeCursorStyle.Render(line)
		} else if e.isSchema() {
			line = treeSchemaStyle.Render(line)
		} else {
			line = treeTableStyle.Render(line)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	content := b.String()
	borderStyle := treeBorderStyle
	if m.focused {
		borderStyle = treeFocusBorder
	}
	return borderStyle.Width(m.width - 2).Height(m.height - 2).Render(content)
}
