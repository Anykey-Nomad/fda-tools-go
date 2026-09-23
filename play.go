package main

import (
	"errors"
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
	finalModel, err := p.Run()
	// Deterministically silence this track before returning: oto's
	// Player.Close() is a no-op, so stop it explicitly. The model is
	// dropped right after, and oto's finalizer closes the player later.
	if fm, ok := finalModel.(playerModel); ok && fm.player != nil {
		fm.player.PauseAndStopReading()
	}
	return err
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

// ---------- chunked PCM buffer ----------

// chunkMillis is the fixed length of one playback chunk (~50 ms). Every
// pause/seek position snaps to a multiple of this quantum, so playback can
// only ever start at a chunk (and therefore sample-frame) boundary.
const chunkMillis = 50

// chunkBuffer holds the decoded PCM split into fixed ~50 ms chunks in a map.
// Chunks are zero-copy slices of the original decode buffer; the map keeps
// the structure O(1)-addressable by index and leaves room for lazily
// loading/evicting chunks later without changing the addressing scheme.
type chunkBuffer struct {
	chunks     map[int][]byte
	chunkBytes int // nominal chunk size; the last chunk may be shorter
	numChunks  int
	totalBytes int
}

func newChunkBuffer(data []byte, sampleRate, channels int) *chunkBuffer {
	bytesPerSample := channels * 2
	// Samples per chunk = sampleRate * 50ms, rounded to a whole sample.
	samples := (sampleRate*chunkMillis + 500) / 1000
	if samples < 1 {
		samples = 1
	}

	cb := &chunkBuffer{
		chunks:     make(map[int][]byte),
		chunkBytes: samples * bytesPerSample,
	}
	for off, i := 0, 0; off < len(data); i++ {
		end := off + cb.chunkBytes
		if end > len(data) {
			end = len(data)
		}
		cb.chunks[i] = data[off:end] // zero copy: subslice of the decode buffer
		off = end
		cb.numChunks = i + 1
	}
	cb.totalBytes = len(data)
	return cb
}

// chunkSource is the io.Reader + io.Seeker that oto consumes. It serves PCM
// straight out of the chunk map and snaps every seek down to a chunk
// boundary, so seeks can never land mid-sample (which produced white noise).
type chunkSource struct {
	cb *chunkBuffer

	mu  sync.Mutex
	idx int // index of the current chunk
	off int // byte offset inside the current chunk
}

func (s *chunkSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	total := 0
	for total < len(p) {
		if s.idx >= s.cb.numChunks {
			return total, io.EOF
		}
		ch := s.cb.chunks[s.idx]
		n := copy(p[total:], ch[s.off:])
		total += n
		s.off += n
		if s.off >= len(ch) {
			s.idx++
			s.off = 0
		}
	}
	return total, nil
}

// bytePosLocked returns the absolute byte position; the caller holds s.mu.
func (s *chunkSource) bytePosLocked() int64 {
	if s.idx >= s.cb.numChunks {
		return int64(s.cb.totalBytes)
	}
	return int64(s.idx*s.cb.chunkBytes + s.off)
}

func (s *chunkSource) bytePos() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytePosLocked()
}

func (s *chunkSource) remaining() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := int64(s.cb.totalBytes) - s.bytePosLocked()
	if n < 0 {
		return 0
	}
	return n
}

// Seek implements io.Seeker (required by oto.Player.Seek) and snaps the
// result down to the nearest chunk boundary.
func (s *chunkSource) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.bytePosLocked() + offset
	case io.SeekEnd:
		abs = int64(s.cb.totalBytes) + offset
	default:
		return 0, errors.New("chunkSource: invalid whence")
	}

	if abs < 0 {
		abs = 0
	}
	if abs >= int64(s.cb.totalBytes) {
		s.idx = s.cb.numChunks
		s.off = 0
		return int64(s.cb.totalBytes), nil
	}
	// Snap down to the chunk grid — the seek quantum.
	abs -= abs % int64(s.cb.chunkBytes)
	s.idx = int(abs) / s.cb.chunkBytes
	s.off = 0
	return abs, nil
}

// layoutKey normalizes a key description so that letter hotkeys work no
// matter which keyboard layout is active. It lowercases the string (Shift
// and CapsLock stop mattering) and maps Cyrillic characters from the
// ЙЦУКЕН layout to the Latin letter of the same physical key
// (ц→q, у→w, к→r, о→j, л→k). Non-letter keys pass through unchanged.
func layoutKey(s string) string {
	s = strings.ToLower(s)
	switch s {
	case "ц":
		return "q"
	case "у":
		return "w"
	case "к":
		return "r"
	case "о":
		return "j"
	case "л":
		return "k"
	}
	return s
}

// ---------- bubbletea player ----------

var (
	playerStyleBar = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	playerStyleDim = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	playerStyleLog = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

type playerTickMsg time.Time

type playerModel struct {
	data        []byte
	sampleRate  int
	channels    int
	wavOut      string
	fda         *FDAFile
	cb          *chunkBuffer
	src         *chunkSource
	player      *oto.Player
	bytesPerSec int64
	total       time.Duration

	// State
	paused     bool
	repeat     bool
	exporting  bool
	exportDone string
	quit       bool
	volumePct  int // 0..200, applied via Player.SetVolume

	// Logs
	logs []string
}

func newPlayerModel(data []byte, sampleRate, channels int, wavOut string, fda *FDAFile) playerModel {
	ctx, err := getOtoContext(sampleRate, channels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Audio init error: %v\n", err)
		os.Exit(1)
	}

	bps := int64(sampleRate * channels * 2)
	cb := newChunkBuffer(data, sampleRate, channels)
	src := &chunkSource{cb: cb}

	// Exactly ONE player per track, created once and never recreated:
	// oto v3.5's Player.Close() is a no-op (cleanup happens via GC
	// finalizer), so re-creating players left "zombie" players reading the
	// same source concurrently — that was the cause of the unstoppable
	// sound, the white noise and the racing track. Pause goes through
	// PauseAndStopReading(), seek through Player.Seek().
	player := ctx.NewPlayer(src)
	player.Play()

	m := playerModel{
		data:        data,
		sampleRate:  sampleRate,
		channels:    channels,
		wavOut:      wavOut,
		fda:         fda,
		cb:          cb,
		src:         src,
		player:      player,
		bytesPerSec: bps,
		total:       time.Duration(float64(len(data)) / float64(bps) * float64(time.Second)),
		volumePct:   100,
	}

	// An oto context lives for the whole process (one format only). Warn if
	// this file's format does not match it — playback would sound wrong.
	if otoSampleRate != sampleRate || otoChannels != channels {
		m.log(fmt.Sprintf("⚠ context is %d Hz/%d ch but file is %d Hz/%d ch",
			otoSampleRate, otoChannels, sampleRate, channels))
	}
	return m
}

func (m *playerModel) log(msg string) {
	m.logs = append(m.logs, time.Now().Format("15:04:05.000")+" "+msg)
	if len(m.logs) > 8 {
		m.logs = m.logs[len(m.logs)-8:]
	}
}

// pos returns the position that has actually been heard: bytes pulled from
// the source minus what is still sitting in oto's hardware buffer, snapped
// down to the chunk grid. While paused nothing moves, so the value freezes
// exactly at the pause point.
func (m *playerModel) pos() time.Duration {
	read := m.src.bytePos()
	buffered := int64(m.player.BufferedSize())
	heard := read - buffered
	if heard < 0 {
		heard = 0
	}
	if heard >= int64(m.cb.totalBytes) {
		return m.total
	}
	if m.cb.chunkBytes > 0 {
		heard -= heard % int64(m.cb.chunkBytes)
	}
	return time.Duration(float64(heard) / float64(m.bytesPerSec) * float64(time.Second))
}

func (m *playerModel) chunkIndex() int64 {
	b := int64(float64(m.pos()) / float64(time.Second) * float64(m.bytesPerSec))
	if m.cb.chunkBytes <= 0 {
		return 0
	}
	return b / int64(m.cb.chunkBytes)
}

// seekBy moves ±delta, snapped to the chunk grid, via Player.Seek. oto
// handles the hard parts: it flushes its internal buffer, waits out any
// in-flight read, discards stale read results, repositions our source and
// keeps playing (or stays paused) exactly as before the seek.
func (m *playerModel) seekBy(delta time.Duration) {
	target := m.pos() + delta
	if target < 0 {
		target = 0
	}
	if target > m.total {
		target = m.total
	}
	b := int64(float64(target) / float64(time.Second) * float64(m.bytesPerSec))
	if m.cb.chunkBytes > 0 {
		b -= b % int64(m.cb.chunkBytes)
	}
	if _, err := m.player.Seek(b, io.SeekStart); err != nil {
		m.log("Seek error: " + err.Error())
		return
	}
	m.log(fmt.Sprintf("Seek %+s → %s (chunk %d)", delta, target.Truncate(time.Millisecond), b/int64(m.cb.chunkBytes)))
}

func (m playerModel) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return playerTickMsg(t)
	})
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
		if !m.paused && m.player != nil && !m.player.IsPlaying() {
			if err := m.player.Err(); err != nil {
				m.log("Audio error: " + err.Error())
				m.quit = true
				return m, tea.Quit
			}
			if m.src.remaining() <= 0 {
				if m.repeat {
					if _, err := m.player.Seek(0, io.SeekStart); err != nil {
						m.log("Repeat error: " + err.Error())
						m.quit = true
						return m, tea.Quit
					}
					m.player.Play()
					m.log("Repeat: restart from chunk 0")
					return m, tickCmd()
				}
				m.log("Track ended")
				m.player.PauseAndStopReading()
				m.quit = true
				return m, tea.Quit
			}
			// Not at EOF but stopped and no error: nudge it back to life.
			m.player.Play()
		}
		return m, tickCmd()

	case exportDoneMsg:
		m.exporting = false
		m.exportDone = msg.path
		if msg.path != "" {
			m.log("Exported: " + msg.path)
		}
		return m, nil

	case tea.KeyMsg:
		key := layoutKey(msg.String())
		switch key {
		case " ":
			if m.paused {
				m.player.Play()
				m.paused = false
				m.log(fmt.Sprintf("▶ resume at %s (chunk %d)", m.pos().Truncate(time.Millisecond), m.chunkIndex()))
			} else {
				m.player.PauseAndStopReading()
				m.paused = true
				m.log(fmt.Sprintf("⏸ pause at %s (chunk %d, oto buf %d B)",
					m.pos().Truncate(time.Millisecond), m.chunkIndex(), m.player.BufferedSize()))
			}
			return m, nil

		case "up", "down":
			step := 10
			if key == "down" {
				step = -10
			}
			m.volumePct += step
			if m.volumePct < 0 {
				m.volumePct = 0
			}
			if m.volumePct > 200 {
				m.volumePct = 200
			}
			m.player.SetVolume(float64(m.volumePct) / 100)
			m.log(fmt.Sprintf("Volume: %d%%", m.volumePct))
			return m, nil

		case "r":
			m.repeat = !m.repeat
			m.log(fmt.Sprintf("Repeat: %v", m.repeat))
			return m, nil

		case "w":
			if !m.exporting {
				m.exporting = true
				m.exportDone = ""
				m.log("Exporting WAV...")
				ch := make(chan string, 1)
				go func() {
					if err := WriteWAV(m.wavOut, &m.fda.Info, m.data); err != nil {
						fmt.Fprintf(os.Stderr, "\nExport error: %v\n", err)
						ch <- ""
					} else {
						ch <- m.wavOut
					}
				}()
				return m, func() tea.Msg {
					result := <-ch
					return exportDoneMsg{path: result}
				}
			}

		case "right", "alt+right", "shift+right":
			delta := 10 * time.Second
			if msg.Alt || strings.HasPrefix(key, "shift+") {
				delta = 30 * time.Second
			}
			m.seekBy(delta)
			return m, nil

		case "left", "alt+left", "shift+left":
			delta := -10 * time.Second
			if msg.Alt || strings.HasPrefix(key, "shift+") {
				delta = -30 * time.Second
			}
			m.seekBy(delta)
			return m, nil

		case "q", "esc", "ctrl+c":
			m.log("Quit")
			m.player.PauseAndStopReading()
			m.quit = true
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m playerModel) View() string {
	p := m.pos()

	barWidth := 40
	filled := 0
	if m.total > 0 {
		filled = int(float64(barWidth) * float64(p) / float64(m.total))
	}
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	state := "▶ "
	if m.paused {
		state = "⏸ "
	}
	volIcon := "🔊"
	if m.volumePct == 0 {
		volIcon = "🔇"
	}
	tags := fmt.Sprintf(" %s%d%%", volIcon, m.volumePct)
	if m.repeat {
		tags += " 🔁"
	}
	if m.exporting {
		tags += " 💾 exporting..."
	}
	if m.exportDone != "" {
		tags += " ✓"
	}

	status := fmt.Sprintf("  %s %s  %s / %s%s",
		state,
		playerStyleBar.Render(bar),
		playerStyleDim.Render(p.Truncate(time.Second).String()),
		playerStyleDim.Render(m.total.Truncate(time.Second).String()),
		playerStyleDim.Render(tags),
	)

	help := playerStyleDim.Render("  Space: pause | ←/→: ±10s | Alt+←/→: ±30s | ↑/↓: volume | R: repeat | W: export WAV | Q: back/quit")

	var logLines strings.Builder
	for _, l := range m.logs {
		logLines.WriteString(playerStyleLog.Render("  📋 "+l) + "\n")
	}

	return "\n" + status + "\n\n" + help + "\n\n" + logLines.String()
}
