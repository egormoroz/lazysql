package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// --- Messages ---

type pageLoadedMsg struct {
	rows    [][]any
	columns []TableColumn
	schema  string
	table   string
	hasMore bool
	err     error
}

// --- Table model ---

// TableModel displays rows with infinite scroll (loads more on demand).
type TableModel struct {
	db       *DB
	pageSize int

	// Current table being viewed.
	schema string
	table  string

	// Accumulated data across fetches.
	columns []TableColumn
	rows    [][]any
	hasMore bool

	// Column display widths.
	colWidths []int

	// Viewport state.
	cursorRow int
	cursorCol int
	scrollRow int
	scrollCol int

	// User-defined filter and sort.
	where   string
	orderBy string

	// Filter input.
	filtering   bool
	filterInput textinput.Model

	// Query cancellation.
	cancel context.CancelFunc

	// Layout.
	width   int
	height  int
	focused bool

	// Status.
	loading bool
	err     error
}

func NewTableModel(db *DB, pageSize int) TableModel {
	fi := textinput.New()
	fi.Placeholder = "WHERE ..."
	fi.CharLimit = 256

	return TableModel{
		db:          db,
		pageSize:    pageSize,
		filterInput: fi,
	}
}

func (m TableModel) Init() tea.Cmd { return nil }

// LoadTable resets state and loads the first batch for a new table.
func (m *TableModel) LoadTable(schema, table string) tea.Cmd {
	m.cancelQuery()
	m.schema = schema
	m.table = table
	m.rows = nil
	m.columns = nil
	m.hasMore = false
	m.loading = true
	m.err = nil
	m.cursorRow = 0
	m.cursorCol = 0
	m.scrollRow = 0
	m.scrollCol = 0
	m.where = ""
	m.orderBy = ""
	m.filterInput.SetValue("")
	m.filtering = false
	return m.fetchMore()
}

// resetData clears accumulated rows and fetches from scratch (filter/sort change).
func (m *TableModel) resetData() tea.Cmd {
	m.cancelQuery()
	m.rows = nil
	m.hasMore = false
	m.loading = true
	m.err = nil
	m.cursorRow = 0
	m.scrollRow = 0
	return m.fetchMore()
}

func (m *TableModel) cancelQuery() {
	if m.cancel != nil {
		slog.Debug("cancelling in-flight query", "schema", m.schema, "table", m.table)
		m.cancel()
		m.cancel = nil
	}
}

func (m *TableModel) fetchMore() tea.Cmd {
	m.cancelQuery()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	q := TableQuery{
		Schema:   m.schema,
		Table:    m.table,
		Where:    m.where,
		OrderBy:  m.orderBy,
		PageSize: m.pageSize,
		Offset:   len(m.rows),
	}
	db := m.db
	return func() tea.Msg {
		page, err := db.FetchPage(ctx, q)
		if err != nil && ctx.Err() != nil {
			slog.Info("query cancelled", "schema", q.Schema, "table", q.Table)
			return pageLoadedMsg{schema: q.Schema, table: q.Table, err: fmt.Errorf("query cancelled")}
		}
		if err != nil {
			return pageLoadedMsg{schema: q.Schema, table: q.Table, err: err}
		}
		return pageLoadedMsg{
			rows:    page.Rows,
			columns: page.Columns,
			schema:  q.Schema,
			table:   q.Table,
			hasMore: page.HasNext,
		}
	}
}

func (m TableModel) Update(msg tea.Msg) (TableModel, tea.Cmd) {
	switch msg := msg.(type) {
	case pageLoadedMsg:
		if msg.schema != m.schema || msg.table != m.table {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			slog.Error("page load failed", "schema", msg.schema, "table", msg.table, "error", msg.err)
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		if m.columns == nil {
			m.columns = msg.columns
		}
		m.rows = append(m.rows, msg.rows...)
		m.hasMore = msg.hasMore
		m.recomputeColWidths(msg.rows)
		return m, nil

	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}
		if m.filtering {
			return m.handleFilterKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m TableModel) handleFilterKey(msg tea.KeyMsg) (TableModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.filterInput.Blur()
		m.filterInput.SetValue(m.where)
		return m, nil
	case "enter":
		m.filtering = false
		m.filterInput.Blur()
		m.where = strings.TrimSpace(m.filterInput.Value())
		return m, m.resetData()
	}

	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	return m, cmd
}

func (m TableModel) handleKey(msg tea.KeyMsg) (TableModel, tea.Cmd) {
	if msg.String() == "esc" && m.loading {
		m.cancelQuery()
		m.loading = false
		m.err = fmt.Errorf("cancelled")
		return m, nil
	}

	if msg.String() == "/" {
		m.filtering = true
		m.filterInput.SetValue(m.where)
		m.filterInput.Focus()
		return m, textinput.Blink
	}

	if m.columns == nil || m.loading {
		return m, nil
	}

	switch msg.String() {
	case "J", "K":
		col := m.columns[m.cursorCol].Name
		dir := "ASC"
		if msg.String() == "K" {
			dir = "DESC"
		}
		m.orderBy = col + " " + dir
		return m, m.resetData()
	default:
		return m.handleNavKey(msg)
	}
}

func (m TableModel) handleNavKey(msg tea.KeyMsg) (TableModel, tea.Cmd) {
	m.moveCursor(msg)

	// Load next batch when cursor hits the last row.
	if m.hasMore && !m.loading && m.cursorRow >= len(m.rows)-1 {
		m.loading = true
		return m, m.fetchMore()
	}
	return m, nil
}

func (m *TableModel) moveCursor(msg tea.KeyMsg) {
	nRows := len(m.rows)
	nCols := len(m.columns)

	switch msg.String() {
	case "j", "down":
		if m.cursorRow < nRows-1 {
			m.cursorRow++
			m.ensureRowVisible()
		}
	case "k", "up":
		if m.cursorRow > 0 {
			m.cursorRow--
			m.ensureRowVisible()
		}
	case "l", "right":
		if m.cursorCol < nCols-1 {
			m.cursorCol++
			m.ensureColVisible()
		}
	case "h", "left":
		if m.cursorCol > 0 {
			m.cursorCol--
			m.ensureColVisible()
		}
	case "ctrl+d":
		half := m.visibleRows() / 2
		m.cursorRow = min(m.cursorRow+half, nRows-1)
		m.ensureRowVisible()
	case "ctrl+u":
		half := m.visibleRows() / 2
		m.cursorRow = max(m.cursorRow-half, 0)
		m.ensureRowVisible()
	case "g":
		m.cursorRow = 0
		m.scrollRow = 0
	case "G":
		m.cursorRow = nRows - 1
		m.ensureRowVisible()
	case "0":
		m.cursorCol = 0
		m.scrollCol = 0
	case "$":
		m.cursorCol = nCols - 1
		m.ensureColVisible()
	}
}

func (m *TableModel) ensureRowVisible() {
	visRows := m.visibleRows()
	if m.cursorRow < m.scrollRow {
		m.scrollRow = m.cursorRow
	} else if m.cursorRow >= m.scrollRow+visRows {
		m.scrollRow = m.cursorRow - visRows + 1
	}
}

func (m *TableModel) ensureColVisible() {
	if m.cursorCol < m.scrollCol {
		m.scrollCol = m.cursorCol
	}
	for m.scrollCol < m.cursorCol {
		usedWidth := 0
		fits := false
		for c := m.scrollCol; c <= m.cursorCol && c < len(m.colWidths); c++ {
			usedWidth += m.colWidths[c] + 3
			if c == m.cursorCol && usedWidth <= m.width-2 {
				fits = true
			}
		}
		if fits {
			break
		}
		m.scrollCol++
	}
}

func (m TableModel) visibleRows() int {
	h := m.height - 6
	if h < 1 {
		h = 1
	}
	return h
}

// recomputeColWidths updates column widths using newly loaded rows.
// On first load, computes from scratch. On subsequent loads, only widens.
func (m *TableModel) recomputeColWidths(newRows [][]any) {
	if m.columns == nil {
		return
	}
	if m.colWidths == nil {
		m.colWidths = make([]int, len(m.columns))
		for i, col := range m.columns {
			m.colWidths[i] = ColumnWidth(col.Name, nil, 4, maxCellWidth)
		}
	}
	for i := range m.columns {
		for _, row := range newRows {
			sw := runewidth.StringWidth(FormatCell(row[i]))
			if sw > m.colWidths[i] && sw <= maxCellWidth {
				m.colWidths[i] = sw
			}
		}
	}
}

// --- View ---

var (
	tableHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	tableCellStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	tableNullStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	tableCursorStyle = lipgloss.NewStyle().Background(lipgloss.Color("8")).Foreground(lipgloss.Color("15"))
	tablePKStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	tableBorderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
	tableFocusBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("12"))
	statusBarStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	inputLabelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

func (m TableModel) View() string {
	if m.schema == "" {
		return m.renderEmpty("Select a table from the sidebar")
	}

	var b strings.Builder

	b.WriteString(m.renderFilterBar())
	b.WriteString("\n")

	if m.loading && len(m.rows) == 0 {
		return m.renderContent(b.String(), "Loading...")
	}
	if m.err != nil && len(m.rows) == 0 {
		return m.renderContent(b.String(), fmt.Sprintf("Error: %s", m.err))
	}
	if len(m.rows) == 0 {
		return m.renderContent(b.String(), "empty")
	}

	m.renderTable(&b)

	b.WriteString("\n")
	b.WriteString(m.statusBar())

	borderStyle := tableBorderStyle
	if m.focused {
		borderStyle = tableFocusBorder
	}
	return borderStyle.Width(m.width - 2).Height(m.height - 2).Render(b.String())
}

func (m TableModel) renderFilterBar() string {
	if m.filtering {
		return inputLabelStyle.Render("WHERE ") + m.filterInput.View()
	}
	if m.where != "" {
		return statusBarStyle.Render("WHERE " + m.where)
	}
	return statusBarStyle.Render("/ to filter")
}

func (m TableModel) renderContent(header, msg string) string {
	borderStyle := tableBorderStyle
	if m.focused {
		borderStyle = tableFocusBorder
	}
	body := header + "\n" + lipgloss.Place(
		m.width-4, m.height-6, lipgloss.Center, lipgloss.Center, msg)
	return borderStyle.Width(m.width - 2).Height(m.height - 2).Render(body)
}

func (m TableModel) renderTable(b *strings.Builder) {
	visRows := m.visibleRows()
	visCols := m.visibleCols()

	b.WriteString(m.renderRow(-1, visCols))
	b.WriteString("\n")

	sepWidth := 0
	for _, c := range visCols {
		sepWidth += m.colWidths[c] + 3
	}
	if sepWidth > 0 {
		sepWidth -= 3
	}
	b.WriteString(strings.Repeat("─", min(sepWidth, m.width-4)))
	b.WriteString("\n")

	endRow := m.scrollRow + visRows
	if endRow > len(m.rows) {
		endRow = len(m.rows)
	}
	for r := m.scrollRow; r < endRow; r++ {
		b.WriteString(m.renderRow(r, visCols))
		if r < endRow-1 {
			b.WriteString("\n")
		}
	}

	rendered := endRow - m.scrollRow
	for i := rendered; i < visRows; i++ {
		b.WriteString("\n~")
	}
}

func (m TableModel) renderRow(rowIdx int, visCols []int) string {
	parts := make([]string, len(visCols))
	for i, c := range visCols {
		w := m.colWidths[c]
		var cell string
		var style lipgloss.Style

		if rowIdx < 0 {
			cell = m.columns[c].Name
			if m.columns[c].IsPK {
				cell += "*"
			}
			style = tableHeaderStyle
		} else {
			raw := m.rows[rowIdx][c]
			cell = FormatCell(raw)
			if raw == nil {
				style = tableNullStyle
			} else if m.columns[c].IsPK {
				style = tablePKStyle
			} else {
				style = tableCellStyle
			}
		}

		cell = runewidth.FillRight(runewidth.Truncate(cell, w, "…"), w)

		if rowIdx >= 0 && rowIdx == m.cursorRow && c == m.cursorCol {
			cell = tableCursorStyle.Render(cell)
		} else {
			cell = style.Render(cell)
		}
		parts[i] = cell
	}
	return strings.Join(parts, " │ ")
}

func (m TableModel) visibleCols() []int {
	if m.columns == nil {
		return nil
	}
	var cols []int
	usedWidth := 0
	for c := m.scrollCol; c < len(m.columns); c++ {
		needed := m.colWidths[c] + 3
		if usedWidth+needed > m.width-4 && len(cols) > 0 {
			break
		}
		cols = append(cols, c)
		usedWidth += needed
	}
	return cols
}

func (m TableModel) statusBar() string {
	nRows := len(m.rows)
	nCols := len(m.columns)

	status := fmt.Sprintf("%s.%s  row %d/%d",
		m.schema, m.table,
		m.cursorRow+1, nRows)
	if m.hasMore {
		status += "+"
	}
	status += fmt.Sprintf("  col %d/%d", m.cursorCol+1, nCols)

	if m.orderBy != "" {
		status += "  sort: " + m.orderBy
	}
	if m.loading {
		status += "  loading..."
	}

	return statusBarStyle.Render(status)
}

func (m TableModel) renderEmpty(msg string) string {
	borderStyle := tableBorderStyle
	if m.focused {
		borderStyle = tableFocusBorder
	}
	content := lipgloss.Place(m.width-4, m.height-4, lipgloss.Center, lipgloss.Center, msg)
	return borderStyle.Width(m.width - 2).Height(m.height - 2).Render(content)
}
