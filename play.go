package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ebitengine/oto/v3"
	"golang.org/x/term"
)

// playCmd implements the --play attribute and the "play" subcommand:
// decode FDA in memory with the Relic codec and play it on the audio
// device, without transcoding to WAV on disk.
//
// Accepted invocation forms (arg is an .fda file):
//
//	fda-tools-go play <file.fda>
//	fda-tools-go --play <file.fda>
//	fda-tools-go <file.fda> --play
//
// Also accepts directories: all .fda files inside are played one by one.
//
// Playback controls:
//
//	Space      — pause / resume
//	Right      — seek forward 10 s
//	Left       — seek backward 10 s
//	Shift+Right — seek forward 30 s
//	Shift+Left  — seek backward 30 s
//	R          — toggle repeat / loop
//	Q / Esc    — skip file
//	Ctrl+C     — quit
func playCmd(args []string) {
	// Collect operands, ignore any flags (e.g. --play passed twice).
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

	// Expand directories into .fda file lists (in place).
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
				path := filepath.Join(item, entry.Name())
				if strings.EqualFold(filepath.Ext(path), ".fda") {
					files = append(files, path)
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

// playFile decodes a single FDA file in memory and plays it on the audio device.
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

	duration := time.Duration(float64(len(pcm)) /
		float64(fda.Info.Channels) / float64(fda.Info.SampleRate) * float64(time.Second))

	fmt.Printf("Playing (%s)... Space: pause, ←/→: seek, R: repeat, Q: skip, Ctrl+C: quit.\n",
		duration.Truncate(time.Second))

	return playLoop(pcmDataToBytes(pcm), int(fda.Info.SampleRate), int(fda.Info.Channels))
}

// ---------- raw-mode key reader ----------

// keyReader reads single keys from the terminal in raw mode.  If the terminal
// cannot be switched to raw mode (e.g. stdin is a pipe), it degrades to
// inactive and playback simply runs without key controls.
type keyReader struct {
	ch       chan keyEvent
	fd       int
	oldState *term.State
	active   bool
}

type keyEvent struct {
	kind  string // "space", "left", "right", "r", "q", "ctrlc", "unknown"
	shift bool
	raw   byte
}

func startKeyReader() *keyReader {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return &keyReader{active: false}
	}
	k := &keyReader{ch: make(chan keyEvent, 16), fd: fd, oldState: old, active: true}
	go k.readLoop()
	return k
}

func (k *keyReader) readLoop() {
	defer close(k.ch)
	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 {
			if err != nil {
				return
			}
			continue
		}

		b := buf[0]

		// Escape sequence: ESC [ ...
		if b == 0x1b && n >= 3 && buf[1] == '[' {
			evt := keyEvent{kind: "unknown"}
			switch buf[2] {
			case 'C':
				evt.kind = "right"
				evt.shift = (n > 3 && buf[3] == ';')
			case 'D':
				evt.kind = "left"
				evt.shift = (n > 3 && buf[3] == ';')
			}
			k.ch <- evt
			continue
		}

		// Single bytes.
		switch {
		case b == ' ':
			k.ch <- keyEvent{kind: "space"}
		case b == 'r' || b == 'R':
			k.ch <- keyEvent{kind: "r"}
		case b == 'q' || b == 'Q':
			k.ch <- keyEvent{kind: "q"}
		case b == 3: // Ctrl+C
			k.ch <- keyEvent{kind: "ctrlc"}
		case unicode.IsPrint(rune(b)):
			k.ch <- keyEvent{kind: "unknown", raw: b}
		}
	}
}

func (k *keyReader) restore() {
	if k.active {
		term.Restore(k.fd, k.oldState)
		k.active = false
	}
}

// ---------- playback core ----------

// seekableReader wraps a byte slice and supports seeking via an offset.
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

func (r *seekableReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.offset + offset
	case io.SeekEnd:
		abs = int64(len(r.data)) + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if abs < 0 {
		abs = 0
	}
	if abs > int64(len(r.data)) {
		abs = int64(len(r.data))
	}
	r.offset = abs
	return abs, nil
}

func (r *seekableReader) remaining() int64 {
	n := int64(len(r.data)) - r.offset
	if n < 0 {
		return 0
	}
	return n
}

// playLoop plays the PCM data, handling pause, seek, repeat, and skip.
func playLoop(data []byte, sampleRate, channels int) error {
	op := &oto.NewContextOptions{}
	op.SampleRate = sampleRate
	op.ChannelCount = channels
	op.Format = oto.FormatSignedInt16LE

	ctx, ready, err := oto.NewContext(op)
	if err != nil {
		return fmt.Errorf("init audio: %w", err)
	}
	<-ready

	keys := startKeyReader()
	defer keys.restore()

	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	bytesPerSecond := int64(sampleRate * channels * 2)
	total := time.Duration(float64(len(data)) / float64(bytesPerSecond) * float64(time.Second))

	paused := false
	repeat := false

	// Each iteration plays from sr.offset to the end (or a seek target).
	sr := &seekableReader{data: data}
	player := ctx.NewPlayer(io.NopCloser(sr))
	defer player.Close()
	player.Play()

	// Returns the current playback position in bytes.
	currentPos := func() int64 {
		buf := int64(player.BufferedSize())
		pos := sr.offset - buf
		if pos < 0 {
			return 0
		}
		return pos
	}

	printStatus := func() {
		if !stdoutTTY {
			return
		}
		pos := currentPos()
		if pos > int64(len(data)) {
			pos = int64(len(data))
		}

		// Progress bar: 30 chars wide.
		barWidth := 30
		filled := 0
		if int64(len(data)) > 0 {
			filled = int(float64(barWidth) * float64(pos) / float64(len(data)))
		}
		if filled > barWidth {
			filled = barWidth
		}

		state := "▶"
		if paused {
			state = "⏸"
		}
		repeatTag := ""
		if repeat {
			repeatTag = " 🔁"
		}

		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		cur := time.Duration(pos / bytesPerSecond * int64(time.Second)).Truncate(time.Second)
		fmt.Printf("\r  %s [%s] %s / %s%s   ",
			state, bar,
			cur, total, repeatTag)
	}

	// rebuildPlayer tears down the current player and creates a new one
	// starting from sr.offset (which has been set by a seek or repeat).
	rebuildPlayer := func() {
		player.Close()
		player = ctx.NewPlayer(io.NopCloser(sr))
		player.Play()
	}

	printStatus()

	for {
		// Check end-of-file.
		if !paused && !player.IsPlaying() {
			// If we are within a small tail margin, the track is done.
			if sr.remaining() <= 0 {
				if repeat {
					sr.Seek(0, io.SeekStart)
					rebuildPlayer()
					printStatus()
					continue
				}
				if stdoutTTY {
					fmt.Println()
				}
				return nil
			}
			// Small tail can happen with oto buffering; just wait a beat.
			time.Sleep(50 * time.Millisecond)
			if !player.IsPlaying() && sr.remaining() <= 0 {
				if repeat {
					sr.Seek(0, io.SeekStart)
					rebuildPlayer()
					printStatus()
					continue
				}
				if stdoutTTY {
					fmt.Println()
				}
				return nil
			}
		}

		select {
		case evt, ok := <-keys.ch:
			if !ok {
				// Reader goroutine stopped — keep playing without controls.
				keys.ch = nil
				continue
			}
			switch evt.kind {
			case "space":
				if paused {
					player.Play()
					paused = false
				} else {
					player.Pause()
					paused = true
				}
				printStatus()

			case "r":
				repeat = !repeat
				printStatus()

			case "right":
				seekDelta := int64(10 * bytesPerSecond)
				if evt.shift {
					seekDelta = 30 * bytesPerSecond
				}
				newOff := currentPos() + seekDelta
				if newOff > int64(len(data)) {
					newOff = int64(len(data))
				}
				sr.Seek(newOff, io.SeekStart)
				player.Close()
				player = ctx.NewPlayer(io.NopCloser(sr))
				if !paused {
					player.Play()
				}
				printStatus()

			case "left":
				seekDelta := int64(-10 * bytesPerSecond)
				if evt.shift {
					seekDelta = -30 * bytesPerSecond
				}
				newOff := currentPos() + seekDelta
				if newOff < 0 {
					newOff = 0
				}
				sr.Seek(newOff, io.SeekStart)
				player.Close()
				player = ctx.NewPlayer(io.NopCloser(sr))
				if !paused {
					player.Play()
				}
				printStatus()

			case "q":
				if stdoutTTY {
					fmt.Println()
				}
				return nil

			case "ctrlc":
				keys.restore()
				if stdoutTTY {
					fmt.Println()
				}
				os.Exit(0)
			}

		case <-time.After(100 * time.Millisecond):
			printStatus()
		}
	}
}
