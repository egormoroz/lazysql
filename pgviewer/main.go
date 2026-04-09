package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- App model: composes tree + table ---

type focus int

const (
	focusTree focus = iota
	focusTable
)

type AppModel struct {
	db       *DB
	tree     TreeModel
	table    TableModel
	focus    focus
	width    int
	height   int
	quitting bool
}

func NewAppModel(db *DB, pageSize int) AppModel {
	return AppModel{
		db:    db,
		tree:  NewTreeModel(db),
		table: NewTableModel(db, pageSize),
		focus: focusTree,
	}
}

func (m AppModel) Init() tea.Cmd {
	return m.tree.Init()
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layoutPanels()
		return m, nil

	case tea.KeyMsg:
		// ctrl+c always quits. Other global keys only apply
		// when no child component is capturing text input.
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if !m.tree.filtering {
			switch msg.String() {
			case "q":
				m.quitting = true
				return m, tea.Quit
			case "tab":
				if m.focus == focusTree {
					m.focus = focusTable
				} else {
					m.focus = focusTree
				}
				m.tree.focused = m.focus == focusTree
				m.table.focused = m.focus == focusTable
				return m, nil
			}
		}

	case tableSelectedMsg:
		slog.Info("table selected", "schema", msg.schema, "table", msg.table)
		m.focus = focusTable
		m.tree.focused = false
		m.table.focused = true
		cmd := m.table.LoadTable(msg.schema, msg.table)
		return m, cmd
	}

	// Dispatch to focused panel.
	var cmds []tea.Cmd

	tree, treeCmd := m.tree.Update(msg)
	m.tree = tree
	if treeCmd != nil {
		cmds = append(cmds, treeCmd)
	}

	table, tableCmd := m.table.Update(msg)
	m.table = table
	if tableCmd != nil {
		cmds = append(cmds, tableCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *AppModel) layoutPanels() {
	treeWidth := 30
	if m.width < 80 {
		treeWidth = 20
	}
	m.tree.width = treeWidth
	m.tree.height = m.height - 1 // leave room for help bar
	m.table.width = m.width - treeWidth
	m.table.height = m.height - 1
}

func (m AppModel) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "Initializing..."
	}

	left := m.tree.View()
	right := m.table.View()

	main := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	help := helpBarStyle.Render(
		"tab: switch panel  j/k: navigate  enter: select  /: filter  n/p: page  esc: cancel  q: quit",
	)

	return lipgloss.JoinVertical(lipgloss.Left, main, help)
}

var helpBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

// --- Entrypoint ---

const defaultPageSize = 100

func connectDB(ctx context.Context, connURL string, cfg *Config) (*DB, int, error) {
	if connURL != "" {
		slog.Info("starting pgviewer", "mode", "url")
		db, err := NewDB(ctx, connURL)
		return db, defaultPageSize, err
	}
	if cfg != nil {
		slog.Info("starting pgviewer", "mode", "config")
		db, err := NewDB(ctx, cfg.Connections[0].URL)
		return db, cfg.PageSize, err
	}
	url := os.Getenv("PGVIEWER_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		return nil, 0, fmt.Errorf(
			"no connection specified: use --url, --config, or set PGVIEWER_URL / DATABASE_URL")
	}
	slog.Info("starting pgviewer", "mode", "env")
	db, err := NewDB(ctx, url)
	return db, defaultPageSize, err
}

func main() {
	configPath := flag.String("config", "", "path to config YAML file")
	connURL := flag.String("url", "", "postgres connection URL (overrides config)")
	logFile := flag.String("log", "", "log file path (default: /tmp/pgviewer.log)")
	flag.Parse()

	ctx := context.Background()

	// Determine log file: flag > config > default.
	logPath := defaultLogFile
	if *logFile != "" {
		logPath = *logFile
	}

	// If using a config file, it may override the log path —
	// parse it first so we can init the logger with the right path.
	var cfg *Config
	if *configPath != "" {
		var err error
		cfg, err = LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *logFile == "" && cfg.LogFile != "" {
			logPath = cfg.LogFile
		}
	}

	// Init logger once. Always returns a valid closer (slog goes
	// to discard on failure so it never corrupts the TUI).
	closeLog, logErr := InitLogger(logPath)
	defer closeLog()
	if logErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open log file: %v\n", logErr)
	}

	db, pageSize, err := connectDB(ctx, *connURL, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	app := NewAppModel(db, pageSize)
	slog.Info("launching TUI")
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		slog.Error("TUI crashed", "error", err)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	slog.Info("pgviewer exited cleanly")
}
