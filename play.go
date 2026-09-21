package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ebitengine/oto/v3"
)

// playCmd implements the --play attribute and the "play" subcommand.
func playCmd(args []string) {
	var items []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		items = append(items, a)
	}

	if len(items) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no input file. Usage: fda-tools-go play <file.fda>\n")
		os.Exit(1)
	}

	var files []string
	for _, item := range items {
		info, err := os.Stat(item)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot access %s: %v\n", item, err)
			continue
		}
		if info.IsDir() {
			entries, err := os.ReadDir(item)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				p := filepath.Join(item, entry.Name())
				if strings.EqualFold(filepath.Ext(p), ".fda") {
					files = append(files, p)
				}
			}
			continue
		}
		files = append(files, item)
	}

	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no .fda files to play\n")
		os.Exit(1)
	}

	for i, file := range files {
		if len(files) > 1 {
			fmt.Printf("[%d/%d] ", i+1, len(files))
		}
		if err := playFile(file); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
}

// playFile decodes a single FDA file in memory and plays it with a bubbletea UI.
func playFile(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".fda" {
		return fmt.Errorf("%s: not an .fda file", path)
	}

	fmt.Printf("Parsing %s...\n", path)
	fda, err := ParseFDA(path)
	if err != nil {
		return err
	}
	fda.PrintInfo()

	fmt.Printf("\nDecoding with Relic Codec...\n")
	pcm, err := DecodeFDAToPCM(fda)
	if err != nil {
		return err
	}

	wavOut := strings.TrimSuffix(path, filepath.Ext(path)) + ".wav"
	data := pcmDataToBytes(pcm)
	sampleRate := int(fda.Info.SampleRate)
	channels := int(fda.Info.Channels)

	m := newPlayerModel(data, sampleRate, channels, wavOut, fda)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

// ---------- oto singleton ----------

var (
	otoCtx        *oto.Context
	otoReady      sync.Once
	otoSampleRate int
	otoChannels   int
)

func getOtoContext(sampleRate, channels int) (*oto.Context, error) {
	var initErr error
	otoReady.Do(func() {
		op := &oto.NewContextOptions{}
		op.SampleRate = sampleRate
		op.ChannelCount = channels
		op.Format = oto.FormatSignedInt16LE

		var ready chan struct{}
		otoCtx, ready, initErr = oto.NewContext(op)
		if initErr == nil {
			<-ready
		}
		otoSampleRate = sampleRate
		otoChannels = channels
	})
	if initErr != nil {
		return nil, initErr
	}
	return otoCtx, nil
}

// ---------- seekable reader ----------

type seekableReader struct {
	data   []byte
	offset int64
}

func (r *seekableReader) Read(p []byte) (int, error) {
	if r.offset >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += int64(n)
	return n, nil
}

func (r *seekableReader) remaining() int64 {
	n := int64(len(r.data)) - r.offset
	if n < 0 {
		return 0
	}
	return n
}

func (r *seekableReader) seekTo(pos int64) {
	if pos < 0 {
		pos = 0
	}
	if pos > int64(len(r.data)) {
		pos = int64(len(r.data))
	}
	r.offset = pos
}

// ---------- bubbletea player ----------

var (
	playerStyleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170"))
	playerStyleBar   = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	playerStyleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	playerStyleOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
)

type playerTickMsg time.Time

type playerModel struct {
	data         []byte
	sampleRate   int
	channels     int
	wavOut       string
	fda          *FDAFile
	sr           *seekableReader
	player       *oto.Player
	ctx          *oto.Context
	bytesPerSec  int64
	total        time.Duration
	paused       bool
	repeat       bool
	exporting    bool
	exportDone   string // non-empty after export finishes
	startTime    time.Time
	pauseOffset  time.Duration
	pauseStarted time.Time
	quit         bool
}

func newPlayerModel(data []byte, sampleRate, channels int, wavOut string, fda *FDAFile) playerModel {
	ctx, err := getOtoContext(sampleRate, channels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Audio init error: %v\n", err)
		os.Exit(1)
	}

	sr := &seekableReader{data: data}
	player := ctx.NewPlayer(io.NopCloser(sr))
	player.Play()

	bps := int64(sampleRate * channels * 2)
	total := time.Duration(float64(len(data)) / float64(bps) * float64(time.Second))

	return playerModel{
		data:        data,
		sampleRate:  sampleRate,
		channels:    channels,
		wavOut:      wavOut,
		fda:         fda,
		sr:          sr,
		player:      player,
		ctx:         ctx,
		bytesPerSec: bps,
		total:       total,
		startTime:   time.Now(),
	}
}

func (m playerModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), waitForExport(nil))
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return playerTickMsg(t)
	})
}

func waitForExport(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		result := <-ch
		return exportDoneMsg{path: result}
	}
}

type exportDoneMsg struct{ path string }

func (m playerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, nil

	case playerTickMsg:
		if m.quit {
			return m, tea.Quit
		}
		// Check end-of-file
		if !m.paused && !m.player.IsPlaying() {
			if m.sr.remaining() <= 0 {
				if m.repeat {
					m.sr.seekTo(0)
					m.player.Close()
					m.player = m.ctx.NewPlayer(io.NopCloser(m.sr))
					m.player.Play()
					m.startTime = time.Now()
					return m, tickCmd()
				}
				return m, tea.Quit
			}
		}
		return m, tickCmd()

	case exportDoneMsg:
		m.exporting = false
		m.exportDone = msg.path
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case " ":
			if m.paused {
				// Resume
				elapsed := m.pauseOffset
				m.player.Close()
				m.sr.seekTo(m.posToBytes(elapsed))
				m.player = m.ctx.NewPlayer(io.NopCloser(m.sr))
				m.player.Play()
				m.startTime = time.Now().Add(-elapsed)
				m.paused = false
			} else {
				// Pause
				m.pauseOffset = m.currentPosition()
				m.player.Close()
				m.paused = true
			}
			return m, nil

		case "r":
			m.repeat = !m.repeat
			return m, nil

		case "w":
			if !m.exporting {
				m.exporting = true
				m.exportDone = ""
				ch := make(chan string, 1)
				go func() {
					if err := WriteWAV(m.wavOut, &m.fda.Info, m.data); err != nil {
						fmt.Fprintf(os.Stderr, "\nExport error: %v\n", err)
						ch <- ""
					} else {
						ch <- m.wavOut
					}
				}()
				return m, waitForExport(ch)
			}

		case "right":
			delta := 10 * time.Second
			if msg.Alt {
				delta = 30 * time.Second
			}
			newPos := m.currentPosition() + delta
			if newPos > m.total {
				newPos = m.total
			}
			if !m.paused {
				m.player.Close()
			}
			m.sr.seekTo(m.posToBytes(newPos))
			if !m.paused {
				m.player = m.ctx.NewPlayer(io.NopCloser(m.sr))
				m.player.Play()
				m.startTime = time.Now().Add(-newPos)
			}
			m.pauseOffset = newPos
			return m, nil

		case "left":
			delta := -10 * time.Second
			if msg.Alt {
				delta = -30 * time.Second
			}
			newPos := m.currentPosition() + delta
			if newPos < 0 {
				newPos = 0
			}
			if !m.paused {
				m.player.Close()
			}
			m.sr.seekTo(m.posToBytes(newPos))
			if !m.paused {
				m.player = m.ctx.NewPlayer(io.NopCloser(m.sr))
				m.player.Play()
				m.startTime = time.Now().Add(-newPos)
			}
			m.pauseOffset = newPos
			return m, nil

		case "q", "esc", "ctrl+c":
			if !m.paused {
				m.player.Close()
			}
			m.quit = true
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m playerModel) currentPosition() time.Duration {
	if m.paused {
		return m.pauseOffset
	}
	pos := time.Since(m.startTime)
	if pos > m.total {
		pos = m.total
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}

// posToBytes converts a time.Duration position to a byte offset in the PCM data.
func (m playerModel) posToBytes(d time.Duration) int64 {
	b := int64(float64(d) * float64(m.bytesPerSec) / float64(time.Second))
	if b < 0 {
		b = 0
	}
	if b > int64(len(m.data)) {
		b = int64(len(m.data))
	}
	return b
}

func (m playerModel) View() string {
	pos := m.currentPosition()
	barWidth := 40
	filled := 0
	if m.total > 0 {
		filled = int(float64(barWidth) * float64(pos) / float64(m.total))
	}
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	state := "▶ "
	if m.paused {
		state = "⏸ "
	}
	tags := ""
	if m.repeat {
		tags += " 🔁"
	}
	if m.exporting {
		tags += " 💾 exporting..."
	}
	if m.exportDone != "" {
		tags += " ✓ exported"
	}

	return fmt.Sprintf("\n  %s %s %s / %s%s\n\n  %s\n",
		state,
		playerStyleBar.Render(bar),
		playerStyleDim.Render(m.currentPosition().Truncate(time.Second).String()),
		playerStyleDim.Render(m.total.Truncate(time.Second).String()),
		playerStyleDim.Render(tags),
		playerStyleHelp(),
	)
}

func playerHelp() string {
	return "Space: pause | ←/→: ±10s | Alt+←/→: ±30s | R: repeat | W: export WAV | Q: quit"
}

func lipglossStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
}

func playerStyleHelp() string {
	return lipglossStyle().Render("  " + playerHelp())
}
