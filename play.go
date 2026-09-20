package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// Playback controls: Space = pause/resume, Q or Esc = skip file,
// Ctrl+C = quit.
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
	fmt.Printf("Playing (%s)... Space: pause/resume, Q: skip, Ctrl+C: quit.\n",
		duration.Truncate(time.Second))

	return playPCM(pcmDataToBytes(pcm), int(fda.Info.SampleRate), int(fda.Info.Channels))
}

// countingReader counts the total number of bytes handed over to the player,
// which (minus the device buffer) equals the playback position.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// keyReader reads single keys from the terminal in raw mode. If the terminal
// cannot be switched to raw mode (e.g. stdin is a pipe), it degrades to
// inactive and playback simply runs without key controls.
type keyReader struct {
	ch       chan byte
	fd       int
	oldState *term.State
	active   bool
}

func startKeyReader() *keyReader {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return &keyReader{active: false}
	}
	k := &keyReader{ch: make(chan byte, 8), fd: fd, oldState: old, active: true}
	go func() {
		defer close(k.ch)
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				k.ch <- buf[0]
			}
			if err != nil {
				return
			}
		}
	}()
	return k
}

func (k *keyReader) restore() {
	if k.active {
		term.Restore(k.fd, k.oldState)
		k.active = false
	}
}

// playPCM feeds 16-bit interleaved PCM to the audio output device and
// blocks until playback finishes. Space toggles pause/resume.
func playPCM(data []byte, sampleRate, channels int) error {
	// Set up the output context for this file's format.
	op := &oto.NewContextOptions{}
	op.SampleRate = sampleRate
	op.ChannelCount = channels
	op.Format = oto.FormatSignedInt16LE

	ctx, ready, err := oto.NewContext(op)
	if err != nil {
		return fmt.Errorf("init audio: %w", err)
	}
	<-ready

	src := &countingReader{r: bytes.NewReader(data)}
	player := ctx.NewPlayer(io.NopCloser(src))
	defer player.Close()

	player.Play()

	keys := startKeyReader()
	defer keys.restore()

	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	bytesPerSecond := int64(sampleRate * channels * 2)
	total := time.Duration(int64(len(data)) / bytesPerSecond * int64(time.Second))

	printStatus := func(paused bool) {
		if !stdoutTTY {
			return
		}
		pos := int64(0)
		if p := src.n - int64(player.BufferedSize()); p > 0 {
			pos = p
		}
		if pos > int64(len(data)) {
			pos = int64(len(data))
		}
		state := "playing"
		if paused {
			state = "paused "
		}
		fmt.Printf("\r  [%s] %s / %s   ",
			state,
			(time.Duration(pos / bytesPerSecond * int64(time.Second))).Truncate(time.Second),
			total)
	}

	paused := false
	for {
		select {
		case b, ok := <-keys.ch:
			if !ok {
				keys.ch = nil // reader stopped; keep playing without controls
				continue
			}
			switch b {
			case ' ':
				if paused {
					player.Play()
					paused = false
				} else {
					player.Pause()
					paused = true
				}
				printStatus(paused)
			case 'q', 'Q', 0x1b: // skip current file
				if stdoutTTY {
					fmt.Println()
				}
				return nil
			case 3: // Ctrl+C (raw mode does not generate SIGINT)
				keys.restore()
				fmt.Println()
				os.Exit(0)
			}
		case <-time.After(100 * time.Millisecond):
			if !paused && !player.IsPlaying() {
				if stdoutTTY {
					fmt.Println()
				}
				return nil
			}
			printStatus(paused)
		}
	}
}
