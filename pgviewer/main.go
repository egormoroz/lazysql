package main

import (
	"context"
	"flag"
	"fmt"
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
		switch msg.String() {
		case "q", "ctrl+c":
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

	case tableSelectedMsg:
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
		"tab: switch panel  j/k: navigate  enter: select  /: filter  n/p: page  q: quit",
	)

	return lipgloss.JoinVertical(lipgloss.Left, main, help)
}

var helpBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

// --- Entrypoint ---

func main() {
	configPath := flag.String("config", "", "path to config YAML file")
	connURL := flag.String("url", "", "postgres connection URL (overrides config)")
	flag.Parse()

	var db *DB
	var pageSize int
	ctx := context.Background()

	switch {
	case *connURL != "":
		pageSize = 100
		var err error
		db, err = NewDB(ctx, *connURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

	case *configPath != "":
		cfg, err := LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		pageSize = cfg.PageSize
		// Use first connection for now.
		db, err = NewDB(ctx, cfg.Connections[0].URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error connecting to %s: %v\n", cfg.Connections[0].Name, err)
			os.Exit(1)
		}

	default:
		// Try PGVIEWER_URL env, then DATABASE_URL.
		url := os.Getenv("PGVIEWER_URL")
		if url == "" {
			url = os.Getenv("DATABASE_URL")
		}
		if url == "" {
			fmt.Fprintln(os.Stderr, "usage: pgviewer --url postgres://... | --config config.yaml")
			fmt.Fprintln(os.Stderr, "  or set PGVIEWER_URL / DATABASE_URL environment variable")
			os.Exit(1)
		}
		pageSize = 100
		var err error
		db, err = NewDB(ctx, url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	defer db.Close()

	app := NewAppModel(db, pageSize)
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
