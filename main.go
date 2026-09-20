package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// No arguments -> interactive browser mode.
	if len(os.Args) < 2 {
		browserMode()
		return
	}

	cmd := os.Args[1]

	// --play attribute in any position: play FDA directly from the console,
	// without transcoding to WAV on disk.
	for _, a := range os.Args[1:] {
		if a == "--play" {
			playCmd(os.Args[1:])
			return
		}
	}

	switch cmd {
	case "play", "p":
		playCmd(os.Args[2:])
	case "decode", "d", "fda2wav":
		os.Args = os.Args[1:] // Shift args
		decodeCmd()
	case "encode", "e", "wav2fda":
		os.Args = os.Args[1:] // Shift args
		encodeCmd()
	default:
		dragDropCmd() // Uses the full os.Args[1:] (plain paths)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "fda-tools-go - FDA audio converter (with Relic Codec)\n\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  fda-tools-go play <file.fda>                - Play FDA directly (no WAV on disk)\n")
	fmt.Fprintf(os.Stderr, "  fda-tools-go decode [options] <input.fda>   - Decode FDA to WAV\n")
	fmt.Fprintf(os.Stderr, "  fda-tools-go encode [options] <input.wav>   - Encode WAV to FDA\n\n")
	fmt.Fprintf(os.Stderr, "Decode options:\n")
	fmt.Fprintf(os.Stderr, "  -info    Show FDA file info without conversion\n")
	fmt.Fprintf(os.Stderr, "  -o       Output WAV file path\n\n")
	fmt.Fprintf(os.Stderr, "Encode options:\n")
	fmt.Fprintf(os.Stderr, "  -b       Block bitrate: 256/512/1024/2048 (default: 2048)\n")
	fmt.Fprintf(os.Stderr, "  -o       Output FDA file path\n")
}

// --- SHARED CONVERSION LOGIC ---

// convertFile converts a single .fda or .wav file to the opposite format.
// If output is empty, the result is written next to the input; the original
// file is never touched.
func convertFile(input, output string, bitrate int) error {
	ext := strings.ToLower(filepath.Ext(input))

	switch ext {
	case ".fda":
		fmt.Printf("Parsing %s...\n", input)
		fda, err := ParseFDA(input)
		if err != nil {
			return err
		}

		fda.PrintInfo()

		if output == "" {
			base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
			output = filepath.Join(filepath.Dir(input), base+".wav")
		}

		fmt.Printf("\nDecoding with Relic Codec...\n")
		if err := DecodeFDAtoWAV(output, fda); err != nil {
			return err
		}

		fmt.Printf("Done! Output: %s\n", output)
		return nil

	case ".wav":
		fmt.Printf("Reading %s...\n", input)
		wav, err := ReadWAV(input)
		if err != nil {
			return err
		}

		fmt.Printf("  SampleRate: %d Hz\n", wav.SampleRate)
		fmt.Printf("  Channels: %d\n", wav.Channels)
		fmt.Printf("  BitsPerSample: %d\n", wav.BitsPerSample)
		fmt.Printf("  Duration: %.1f seconds\n",
			float64(len(wav.PCMData))/(float64(wav.SampleRate)*float64(wav.Channels)*2))

		if wav.BitsPerSample != 16 {
			return fmt.Errorf("only 16-bit WAV files are supported")
		}

		if output == "" {
			base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
			output = filepath.Join(filepath.Dir(input), base+".fda")
		}

		fmt.Printf("\nEncoding with Relic Codec (bitrate=%d)...\n", bitrate)
		if err := EncodeWAVtoFDA(output, wav, bitrate); err != nil {
			return err
		}

		fmt.Printf("Done! Output: %s\n", output)
		return nil

	default:
		return fmt.Errorf("unsupported file type %q (expected .fda or .wav)", ext)
	}
}

// convertDirectory converts every .fda and .wav file directly inside dir
// (non-recursive). Returns the number of files converted and the number skipped.
func convertDirectory(dir string, bitrate int) (converted, skipped int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".fda" && ext != ".wav" {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		// Skip if the opposite-format file already exists.
		opposite := ".fda"
		if ext == ".fda" {
			opposite = ".wav"
		}
		oppositePath := strings.TrimSuffix(path, filepath.Ext(path)) + opposite
		if _, err := os.Stat(oppositePath); err == nil {
			fmt.Printf("Skipping %s: %s already exists\n", path, oppositePath)
			skipped++
			continue
		}

		if err := convertFile(path, "", bitrate); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}
		converted++
	}

	return converted, skipped, nil
}

// --- DRAG & DROP ---

// dragDropCmd handles Drag & Drop files/folders from Explorer, i.e. plain
// paths passed as command-line arguments.
func dragDropCmd() {
	var bitrate int
	fs := flag.NewFlagSet("dragdrop", flag.ContinueOnError)
	fs.IntVar(&bitrate, "b", 2048, "Block bitrate for WAV to FDA (256/512/1024/2048)")
	fs.Parse(os.Args[1:])
	if bitrate != 256 && bitrate != 512 && bitrate != 1024 && bitrate != 2048 {
		fmt.Fprintf(os.Stderr, "Error: invalid bitrate %d. Must be 256, 512, 1024, or 2048\n", bitrate)
		os.Exit(1)
	}

	// Arguments that follow known flags still land in fs.Args() thanks to
	// Go's flag package (flags are only recognized with a - prefix).
	items := fs.Args()
	if len(items) == 0 {
		printUsage()
		pauseBeforeExit()
		os.Exit(1)
	}

	fmt.Printf("fda-tools-go Drag & Drop mode (bitrate=%d)\n\n", bitrate)

	totalConverted, totalSkipped, totalFailed := 0, 0, 0

	for _, item := range items {
		info, err := os.Stat(item)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot access %s: %v\n", item, err)
			totalFailed++
			continue
		}

		if info.IsDir() {
			fmt.Printf("Folder: %s\n", item)
			converted, skipped, err := convertDirectory(item, bitrate)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				totalFailed++
				continue
			}
			totalConverted += converted
			totalSkipped += skipped
			fmt.Printf("Folder done: %d converted, %d skipped\n\n", converted, skipped)
		} else {
			ext := strings.ToLower(filepath.Ext(item))
			if ext != ".fda" && ext != ".wav" {
				fmt.Fprintf(os.Stderr, "Skipping %s: unsupported file type\n", item)
				continue
			}
			if err := convertFile(item, "", bitrate); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				totalFailed++
				continue
			}
			totalConverted++
			fmt.Println()
		}
	}

	fmt.Printf("All done: %d converted, %d skipped, %d failed\n", totalConverted, totalSkipped, totalFailed)

	pauseBeforeExit()
}

// pauseBeforeExit waits for Enter so the console window (e.g. opened by
// Explorer for a Drag & Drop) doesn't close before the user sees the result.
func pauseBeforeExit() {
	fmt.Print("\nPress Enter to exit...")
	fmt.Scanln()
}

// --- DECODE (fda2wav) ---

func decodeCmd() {
	var (
		showInfo bool
		output   string
	)

	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	fs.BoolVar(&showInfo, "info", false, "Show FDA file info without conversion")
	fs.StringVar(&output, "o", "", "Output WAV file path (default: input with .wav extension)")
	fs.Parse(os.Args[1:])

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	input := fs.Arg(0)

	if showInfo {
		fda, err := ParseFDA(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fda.PrintInfo()
		return
	}

	if err := convertFile(input, output, 2048); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// --- ENCODE (wav2fda) ---

func encodeCmd() {
	var (
		bitrate int
		output  string
	)

	fs := flag.NewFlagSet("encode", flag.ExitOnError)
	fs.IntVar(&bitrate, "b", 2048, "Block bitrate (256/512/1024/2048)")
	fs.StringVar(&output, "o", "", "Output FDA file path (default: input with .fda extension)")
	fs.Parse(os.Args[1:])

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	input := fs.Arg(0)

	if bitrate != 256 && bitrate != 512 && bitrate != 1024 && bitrate != 2048 {
		fmt.Fprintf(os.Stderr, "Error: invalid bitrate %d. Must be 256, 512, 1024, or 2048\n", bitrate)
		os.Exit(1)
	}

	if err := convertFile(input, output, bitrate); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// Suppress unused import warning for math in wav2fda.go
var _ = filepath.Join
