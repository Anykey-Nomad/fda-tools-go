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
	fmt.Printf("Playing (%s)... Press Ctrl+C to stop.\n", duration)

	return playPCM(pcmDataToBytes(pcm), int(fda.Info.SampleRate), int(fda.Info.Channels))
}

// playPCM feeds 16-bit interleaved PCM to the audio output device and
// blocks until playback finishes.
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

	player := ctx.NewPlayer(io.NopCloser(bytes.NewReader(data)))
	defer player.Close()

	player.Play()

	// Wait until playback is done (oto has no completion callback).
	for player.IsPlaying() {
		time.Sleep(50 * time.Millisecond)
	}

	return nil
}
