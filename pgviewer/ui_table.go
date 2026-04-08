package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Messages ---

type pageLoadedMsg struct {
	page   *KeysetPage
	schema string
	table  string
	err    error
}

// --- Table model ---

// TableModel displays rows from a table with keyset pagination.
type TableModel struct {
	db       *DB
	pageSize int

	// Current table being viewed.
	schema string
	table  string

	// Current page data.
	page *KeysetPage

	// Column display widths.
	colWidths []int

	// Viewport state.
	cursorRow int // selected row
	cursorCol int // selected column
	scrollRow int // first visible row
	scrollCol int // first visible column

	// Pagination cursors: history of page boundaries for backward navigation.
	pageNum     int
	firstCursor []any // cursor to go backward from current page
	lastCursor  []any // cursor to go forward from current page

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
	return TableModel{
		db:       db,
		pageSize: pageSize,
	}
}

func (m TableModel) Init() tea.Cmd { return nil }

// LoadTable initiates loading data for the given table.
func (m *TableModel) LoadTable(schema, table string) tea.Cmd {
	// Cancel any in-flight query for the previous table.
	m.cancelQuery()
	m.schema = schema
	m.table = table
	m.loading = true
	m.err = nil
	m.pageNum = 0
	m.cursorRow = 0
	m.cursorCol = 0
	m.scrollRow = 0
	m.scrollCol = 0
	m.firstCursor = nil
	m.lastCursor = nil
	return m.newFetch(DirFirst, nil)
}

// cancelQuery cancels any in-flight query.
func (m *TableModel) cancelQuery() {
	if m.cancel != nil {
		slog.Debug("cancelling in-flight query", "schema", m.schema, "table", m.table)
		m.cancel()
		m.cancel = nil
	}
}

// newFetch cancels any previous query, creates a new cancellable context,
// stores its cancel func, and returns a tea.Cmd that runs the query.
func (m *TableModel) newFetch(dir Direction, cursor []any) tea.Cmd {
	m.cancelQuery()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	db := m.db
	schema, table := m.schema, m.table
	pageSize := m.pageSize
	return func() tea.Msg {
		page, err := db.FetchPage(ctx, schema, table, pageSize, dir, cursor)
		if err != nil && ctx.Err() != nil {
			slog.Info("query cancelled", "schema", schema, "table", table)
			return pageLoadedMsg{schema: schema, table: table, err: fmt.Errorf("query cancelled")}
		}
		return pageLoadedMsg{page: page, schema: schema, table: table, err: err}
	}
}

func (m TableModel) Update(msg tea.Msg) (TableModel, tea.Cmd) {
	switch msg := msg.(type) {
	case pageLoadedMsg:
		// Ignore stale loads for a different table.
		if msg.schema != m.schema || msg.table != m.table {
			slog.Debug("ignoring stale page load", "got", msg.schema+"."+msg.table, "want", m.schema+"."+m.table)
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			slog.Error("page load failed", "schema", msg.schema, "table", msg.table, "error", msg.err)
			m.err = msg.err
			m.page = nil
			return m, nil
		}
		m.page = msg.page
		m.err = nil
		m.computeColWidths()
		m.cursorRow = 0
		m.scrollRow = 0
		m.firstCursor = msg.page.FirstCursor
		m.lastCursor = msg.page.LastCursor
		return m, nil

	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m TableModel) handleKey(msg tea.KeyMsg) (TableModel, tea.Cmd) {
	// Escape cancels in-flight queries regardless of state.
	if msg.String() == "esc" && m.loading {
		m.cancelQuery()
		m.loading = false
		m.err = fmt.Errorf("cancelled")
		return m, nil
	}

	if m.page == nil || m.loading {
		return m, nil
	}
	nRows := len(m.page.Rows)
	nCols := len(m.page.Columns)

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
	case "n": // next page
		if m.page.HasNext && m.lastCursor != nil {
			m.loading = true
			m.pageNum++
			return m, m.newFetch(DirForward, m.lastCursor)
		}
	case "p": // previous page
		if m.page.HasPrev && m.firstCursor != nil {
			m.loading = true
			m.pageNum--
			return m, m.newFetch(DirBackward, m.firstCursor)
		}
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

	return m, nil
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
	// Simple: ensure cursorCol is within the visible column window.
	if m.cursorCol < m.scrollCol {
		m.scrollCol = m.cursorCol
	}
	// Scroll right until column fits.
	for m.scrollCol < m.cursorCol {
		usedWidth := 0
		fits := false
		for c := m.scrollCol; c <= m.cursorCol && c < len(m.colWidths); c++ {
			usedWidth += m.colWidths[c] + 3 // 3 for " | " separator
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
	h := m.height - 5 // header row + border + status bar + padding
	if h < 1 {
		h = 1
	}
	return h
}

func (m *TableModel) computeColWidths() {
	if m.page == nil {
		return
	}
	m.colWidths = make([]int, len(m.page.Columns))
	for i, col := range m.page.Columns {
		// Sample up to 50 rows for width estimation.
		samples := make([]string, 0, min(len(m.page.Rows), 50))
		for j := 0; j < len(m.page.Rows) && j < 50; j++ {
			samples = append(samples, FormatCell(m.page.Rows[j][i]))
		}
		m.colWidths[i] = ColumnWidth(col.Name, samples, 4, maxCellWidth)
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
)

func (m TableModel) View() string {
	if m.schema == "" {
		return m.renderEmpty("Select a table from the sidebar")
	}
	if m.loading {
		return m.renderEmpty("Loading...")
	}
	if m.err != nil {
		return m.renderEmpty(fmt.Sprintf("Error: %s", m.err))
	}
	if m.page == nil || len(m.page.Rows) == 0 {
		return m.renderEmpty(fmt.Sprintf("%s.%s: empty", m.schema, m.table))
	}

	var b strings.Builder
	visRows := m.visibleRows()
	visCols := m.visibleCols()

	// Header row.
	headerLine := m.renderRow(-1, visCols)
	b.WriteString(headerLine)
	b.WriteString("\n")

	// Separator.
	sepWidth := 0
	for _, c := range visCols {
		sepWidth += m.colWidths[c] + 3
	}
	if sepWidth > 0 {
		sepWidth -= 3 // no trailing separator
	}
	b.WriteString(strings.Repeat("─", min(sepWidth, m.width-4)))
	b.WriteString("\n")

	// Data rows.
	endRow := m.scrollRow + visRows
	if endRow > len(m.page.Rows) {
		endRow = len(m.page.Rows)
	}
	for r := m.scrollRow; r < endRow; r++ {
		b.WriteString(m.renderRow(r, visCols))
		if r < endRow-1 {
			b.WriteString("\n")
		}
	}

	// Pad remaining height.
	rendered := endRow - m.scrollRow
	for i := rendered; i < visRows; i++ {
		b.WriteString("\n~")
	}

	// Status bar.
	b.WriteString("\n")
	b.WriteString(m.statusBar())

	content := b.String()
	borderStyle := tableBorderStyle
	if m.focused {
		borderStyle = tableFocusBorder
	}
	return borderStyle.Width(m.width - 2).Height(m.height - 2).Render(content)
}

func (m TableModel) renderRow(rowIdx int, visCols []int) string {
	parts := make([]string, len(visCols))
	for i, c := range visCols {
		w := m.colWidths[c]
		var cell string
		var style lipgloss.Style

		if rowIdx < 0 {
			// Header.
			cell = m.page.Columns[c].Name
			if m.page.Columns[c].IsPK {
				cell += " 🔑"
			}
			style = tableHeaderStyle
		} else {
			// Data.
			raw := m.page.Rows[rowIdx][c]
			cell = FormatCell(raw)
			if raw == nil {
				style = tableNullStyle
			} else if m.page.Columns[c].IsPK {
				style = tablePKStyle
			} else {
				style = tableCellStyle
			}
		}

		// Pad/truncate to column width.
		runes := []rune(cell)
		if len(runes) > w {
			cell = string(runes[:w-1]) + "…"
		} else if len(runes) < w {
			cell = cell + strings.Repeat(" ", w-len(runes))
		}

		// Highlight cursor cell.
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
	if m.page == nil {
		return nil
	}
	var cols []int
	usedWidth := 0
	for c := m.scrollCol; c < len(m.page.Columns); c++ {
		needed := m.colWidths[c] + 3 // " | "
		if usedWidth+needed > m.width-4 && len(cols) > 0 {
			break
		}
		cols = append(cols, c)
		usedWidth += needed
	}
	return cols
}

func (m TableModel) statusBar() string {
	if m.page == nil {
		return ""
	}
	nRows := len(m.page.Rows)
	nCols := len(m.page.Columns)

	nav := ""
	if m.page.HasPrev {
		nav += "[p]prev "
	}
	if m.page.HasNext {
		nav += "[n]next "
	}

	return statusBarStyle.Render(fmt.Sprintf(
		"%s.%s  row %d/%d  col %d/%d  page %d  %s",
		m.schema, m.table,
		m.cursorRow+1, nRows,
		m.cursorCol+1, nCols,
		m.pageNum+1,
		nav,
	))
}

func (m TableModel) renderEmpty(msg string) string {
	borderStyle := tableBorderStyle
	if m.focused {
		borderStyle = tableFocusBorder
	}
	content := lipgloss.Place(m.width-4, m.height-4, lipgloss.Center, lipgloss.Center, msg)
	return borderStyle.Width(m.width - 2).Height(m.height - 2).Render(content)
}
