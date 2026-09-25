package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	browserTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170")).Padding(0, 1)
	browserCrumb  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Padding(0, 1)
	browserItem   = lipgloss.NewStyle().PaddingLeft(2)
	browserSel    = lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Color("212")).Bold(true)
	browserHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(1, 2)
	browserInfo   = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).PaddingLeft(2)
	browserNotice = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).PaddingLeft(2)
)

// browserEntry is one selectable row: an on-disk file, or — while an SGA
// archive is open — a ToC root, a directory or a file inside the archive.
type browserEntry struct {
	name   string
	isDir  bool   // directory (on disk this is never set: we only list files)
	toc    bool   // archive ToC root pseudo-entry (dirID holds the root dir id)
	dirID  int    // inside archive: directory id
	fileID int    // inside archive: file id
	size   int64  // archive files: unpacked size; archive dirs: subdir count
	comp   int64  // archive files: packed size; archive dirs: file count
	fsPath string // non-empty for on-disk files
	inSGA  bool
}

// browserModel is the bubbletea model for the FDA file browser. It shows
// either the current directory on disk (.fda/.sga files) or, once an archive
// is opened, its internal directory tree — with the same keys throughout.
type browserModel struct {
	cwd    string
	width  int
	height int

	// archive navigation (sga == nil → browsing the disk)
	sga      *SGAFile
	sgaPath  string
	autoRoot bool  // single ToC was entered automatically
	stack    []int // dir ids inside the archive; empty = ToC-pick level

	entries []browserEntry
	cursor  int

	// selection handed over to browserRunLoop
	selected string   // on-disk .fda path → playFile
	selFDA   *FDAFile // in-archive FDA → playFDA
	selName  string
	notice   string // transient error/hint line

	// SGA write (RebuildSGA) is deferred — kept for future functionality.
	// The FDA→WAV conversion below always writes outside the archive.
}

func browserInit() browserModel {
	cwd, _ := os.Getwd()
	m := browserModel{cwd: cwd}
	m.refreshEntries()
	return m
}

// refreshEntries rebuilds the current listing (disk or archive level).
func (m *browserModel) refreshEntries() {
	m.entries = nil
	m.cursor = 0

	if m.sga == nil {
		for _, name := range listBrowserFiles(m.cwd) {
			m.entries = append(m.entries, browserEntry{
				name:   name,
				size:   -1,
				fsPath: filepath.Join(m.cwd, name),
			})
		}
		return
	}

	if len(m.stack) == 0 {
		// ToC-pick level.
		for i := range m.sga.tocs {
			m.entries = append(m.entries, browserEntry{
				name:  m.sga.tocs[i].alias,
				isDir: true,
				toc:   true,
				dirID: int(m.sga.tocs[i].startDir),
				inSGA: true,
				size:  -1,
			})
		}
		return
	}

	list, err := m.sga.List(m.stack[len(m.stack)-1])
	if err != nil {
		m.notice = err.Error()
		return
	}
	for _, e := range list {
		m.entries = append(m.entries, browserEntry{
			name:   e.Name,
			isDir:  e.IsDir,
			dirID:  e.DirID,
			fileID: e.FileID,
			size:   e.Size,
			comp:   e.CompSize,
			inSGA:  true,
		})
	}
	// For directories comp/Files were carried in Size/CompSize? No: List
	// returns dirs with Size = subdir count and Files = file count; mirror
	// them into the fields View expects.
	for i, e := range list {
		if e.IsDir {
			m.entries[i].size = e.Size
			m.entries[i].comp = int64(e.Files)
		}
	}
}

// openArchive opens an .sga file and switches the browser into it. A single
// ToC archive is entered directly (no pointless extra level).
func (m *browserModel) openArchive(path string) error {
	sga, err := OpenSGA(path)
	if err != nil {
		return err
	}
	if m.sga != nil {
		m.sga.Close()
	}
	m.sga = sga
	m.sgaPath = path
	m.stack = nil
	m.notice = ""
	m.autoRoot = sga.ToCCount() == 1
	if m.autoRoot {
		root, err := sga.RootDir(0)
		if err != nil {
			return err
		}
		m.stack = []int{root}
	}
	m.refreshEntries()
	return nil
}

// closeArchive returns to disk browsing.
func (m *browserModel) closeArchive() {
	if m.sga != nil {
		m.sga.Close()
		m.sga = nil
	}
	m.sgaPath = ""
	m.stack = nil
	m.autoRoot = false
	m.notice = ""
	m.refreshEntries()
}

// goBack moves one level up: inside a directory → its parent; at the archive
// root (or ToC level) → back to the disk listing.
func (m *browserModel) goBack() {
	if m.sga == nil {
		return
	}
	if len(m.stack) > 0 {
		m.stack = m.stack[:len(m.stack)-1]
		if len(m.stack) == 0 && m.autoRoot {
			m.closeArchive()
			return
		}
		m.refreshEntries()
		return
	}
	m.closeArchive()
}

// activate handles Enter on the selected entry.
func (m *browserModel) activate() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	e := m.entries[m.cursor]
	m.notice = ""

	if m.sga == nil {
		switch strings.ToLower(filepath.Ext(e.name)) {
		case ".sga":
			if err := m.openArchive(e.fsPath); err != nil {
				m.notice = "Cannot open archive: " + err.Error()
			}
		case ".fda":
			m.selected = e.fsPath
			return tea.Quit
		}
		return nil
	}

	switch {
	case e.toc, e.isDir:
		m.stack = append(m.stack, e.dirID)
		m.refreshEntries()
	case strings.EqualFold(filepath.Ext(e.name), ".fda"):
		data, err := m.sga.ReadFile(e.fileID)
		if err != nil {
			m.notice = "Cannot extract: " + err.Error()
			return nil
		}
		fda, err := ParseFDAReader(bytes.NewReader(data))
		if err != nil {
			m.notice = "Not a valid FDA: " + err.Error()
			return nil
		}
		// Hand the parsed FDA to browserRunLoop; the browser program ends
		// so the player becomes the only owner of stdin (same strict
		// alternation as for on-disk files).
		m.selFDA = fda
		m.selName = e.name
		return tea.Quit
	default:
		m.notice = fmt.Sprintf("%s: only .fda files can be played", e.name)
	}
	return nil
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
		key := layoutKey(msg.String())
		m.notice = ""
		switch key {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "enter":
			if cmd := m.activate(); cmd != nil {
				return m, cmd
			}
		case "left", "backspace":
			m.goBack()
		case "q", "esc", "ctrl+c":
			// From the browser Q exits the whole program.
			m.selected = ""
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m browserModel) View() string {
	var b strings.Builder
	b.WriteString("\n")

	if m.sga != nil {
		b.WriteString(browserTitle.Render("🗄 " + filepath.Base(m.sgaPath)))
		b.WriteString("\n")
		b.WriteString(browserCrumb.Render("  " + m.breadcrumb()))
	} else {
		b.WriteString(browserTitle.Render("🎵 fda-tools-go — File Browser"))
	}
	b.WriteString("\n\n")

	if len(m.entries) == 0 {
		b.WriteString(browserItem.Render("  (empty)"))
		b.WriteString("\n")
	}

	// Listing with cursor
	maxShow := m.height - 9
	if maxShow < 5 {
		maxShow = 5
	}
	if maxShow > len(m.entries) {
		maxShow = len(m.entries)
	}

	start := 0
	if m.cursor >= maxShow {
		start = m.cursor - maxShow + 1
	}

	for i := 0; i < maxShow; i++ {
		idx := start + i
		if idx >= len(m.entries) {
			break
		}
		line := entryIcon(m.entries[idx]) + " " + m.entries[idx].name
		if idx == m.cursor {
			b.WriteString(browserSel.Render("▸ " + line))
		} else {
			b.WriteString(browserItem.Render("  " + line))
		}
		b.WriteString("\n")
	}

	// Info about the selected entry
	b.WriteString("\n")
	if m.cursor < len(m.entries) {
		e := m.entries[m.cursor]
		switch {
		case e.toc:
			b.WriteString(browserInfo.Render("  📚 ToC: " + e.name))
		case e.inSGA && e.isDir:
			b.WriteString(browserInfo.Render(fmt.Sprintf(
				"  📁 %s  (%d folders, %d files)", e.name, e.size, e.comp)))
		case e.inSGA:
			b.WriteString(browserInfo.Render(fmt.Sprintf(
				"  🎵 %s  (%s → %s)", e.name, fmtSize(e.comp), fmtSize(e.size))))
		default:
			if info, err := os.Stat(e.fsPath); err == nil {
				b.WriteString(browserInfo.Render(fmt.Sprintf(
					"  📄 %s  (%s)", e.name, fmtSize(info.Size()))))
			}
		}
		b.WriteString("\n")
	}
	if m.notice != "" {
		b.WriteString(browserNotice.Render("  ⚠ " + m.notice))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	help := "  ↑↓: select   Enter: open/play"
	if m.sga != nil {
		help += "   ←/Backspace: back"
		// FDA→WAV (W in player) saves outside the archive — see play.go
	}
	help += "   Q: quit"
	b.WriteString(browserHelp.Render(help))

	return b.String()
}

// breadcrumb renders the path inside the archive (dir names are full paths).
func (m browserModel) breadcrumb() string {
	parts := []string{filepath.Base(m.sgaPath)}
	if len(m.stack) > 0 {
		if name := m.sga.dirs[m.stack[len(m.stack)-1]].name; name != "" {
			parts = append(parts, strings.ReplaceAll(name, `\`, " / "))
		}
	}
	return strings.Join(parts, "  ›  ")
}

func entryIcon(e browserEntry) string {
	switch {
	case e.toc:
		return "📚"
	case e.isDir:
		return "📁"
	case e.inSGA:
		if strings.EqualFold(filepath.Ext(e.name), ".fda") {
			return "🎵"
		}
		return "📄"
	default:
		switch strings.ToLower(filepath.Ext(e.name)) {
		case ".fda":
			return "🎵"
		case ".sga":
			return "🗄"
		}
		return "📄"
	}
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// browserMode runs the interactive file browser over the current directory.
func browserMode() {
	m := browserInit()
	if len(m.entries) == 0 {
		fmt.Println("No .fda or .sga files found in the current directory.")
		return
	}
	browserRunLoop(m)
}

// browserModeSGA starts the browser directly inside an SGA archive that was
// dropped onto the executable.
func browserModeSGA(path string) {
	m := browserInit()
	if err := m.openArchive(path); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		pauseBeforeExit()
		os.Exit(1)
	}
	browserRunLoop(m)
}

// browserRunLoop alternates strictly between ONE browser tea.Program and
// ONE player tea.Program: pressing Enter ends the browser, the file plays,
// and the browser restarts with its state restored. This way no two programs
// ever read stdin at the same time.
func browserRunLoop(m browserModel) {
	for {
		p := tea.NewProgram(m, tea.WithAltScreen())
		result, err := p.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if rm, ok := result.(browserModel); ok {
			m = rm // keep cursor, archive stack and notices
		}

		switch {
		case m.selFDA != nil:
			fda, name := m.selFDA, m.selName
			m.selFDA, m.selName = nil, ""
			if err := playFDA(fda, name); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		case m.selected != "":
			path := m.selected
			m.selected = ""
			if err := playFile(path); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		default:
			return // Q / Esc / Ctrl+C in the browser — exit the program
		}
	}
}

// listBrowserFiles returns sorted .fda and .sga filenames in dir.
func listBrowserFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".fda" || ext == ".sga" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}
