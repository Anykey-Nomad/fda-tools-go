package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	browserTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170")).Padding(0, 1)
	browserItem  = lipgloss.NewStyle().PaddingLeft(2)
	browserSel   = lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Color("212")).Bold(true)
	browserHelp  = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(1, 2)
	browserInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).PaddingLeft(2)
)

// browserModel is the bubbletea model for the FDA file browser.
type browserModel struct {
	files   []string
	cwd     string
	cursor  int
	playing bool
	width   int
	height  int
}

func browserInit() browserModel {
	cwd, _ := os.Getwd()
	files := listFDAFiles(cwd)
	return browserModel{files: files, cwd: cwd}
}

func (m browserModel) Init() tea.Cmd {
	return nil
}

func (m browserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.files)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.files) == 0 {
				return m, tea.Quit
			}
			return m, m.playSelected()
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m browserModel) playSelected() tea.Cmd {
	return func() tea.Msg {
		playPath := filepath.Join(m.cwd, m.files[m.cursor])
		if err := playFile(playPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return playDoneMsg{}
	}
}

type playDoneMsg struct{}

func (m browserModel) View() string {
	if len(m.files) == 0 {
		return "\n  No .fda files found in the current directory.\n\n"
	}

	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(browserTitle.Render("🎵 fda-tools-go — File Browser"))
	b.WriteString("\n\n")

	// File list with cursor
	maxShow := m.height - 8
	if maxShow < 5 {
		maxShow = 5
	}
	if maxShow > len(m.files) {
		maxShow = len(m.files)
	}

	start := 0
	if m.cursor >= maxShow {
		start = m.cursor - maxShow + 1
	}

	for i := 0; i < maxShow; i++ {
		idx := start + i
		if idx >= len(m.files) {
			break
		}
		if idx == m.cursor {
			b.WriteString(browserSel.Render("▸ " + m.files[idx]))
		} else {
			b.WriteString(browserItem.Render("  " + m.files[idx]))
		}
		b.WriteString("\n")
	}

	// Info about selected file
	b.WriteString("\n")
	if info, err := os.Stat(filepath.Join(m.cwd, m.files[m.cursor])); err == nil {
		b.WriteString(browserInfo.Render(fmt.Sprintf("  📄 %s  (%.1f KB)", m.files[m.cursor], float64(info.Size())/1024.0)))
	}

	b.WriteString("\n\n")
	b.WriteString(browserHelp.Render("  ↑↓ Navigate   Enter: Play   Q: Quit"))

	return b.String()
}

// browserMode runs the interactive file browser.
func browserMode() {
	m := browserInit()
	if len(m.files) == 0 {
		fmt.Println("No .fda files found in the current directory.")
		return
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// listFDAFiles returns sorted .fda filenames in the given directory.
func listFDAFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".fda") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}
