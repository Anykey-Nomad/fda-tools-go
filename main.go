package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	os.Args = os.Args[1:] // Shift args

	switch cmd {
	case "decode", "d", "fda2wav":
		decodeCmd()
	case "encode", "e", "wav2fda":
		encodeCmd()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "fda2wav - FDA audio converter (with Relic Codec)\n\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  fda2wav decode [options] <input.fda>   - Decode FDA to WAV\n")
	fmt.Fprintf(os.Stderr, "  fda2wav encode [options] <input.wav>   - Encode WAV to FDA\n\n")
	fmt.Fprintf(os.Stderr, "Decode options:\n")
	fmt.Fprintf(os.Stderr, "  -info    Show FDA file info without conversion\n")
	fmt.Fprintf(os.Stderr, "  -o       Output WAV file path\n\n")
	fmt.Fprintf(os.Stderr, "Encode options:\n")
	fmt.Fprintf(os.Stderr, "  -b       Block bitrate: 256/512/1024/2048 (default: 2048)\n")
	fmt.Fprintf(os.Stderr, "  -o       Output FDA file path\n")
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

	fmt.Printf("Parsing %s...\n", input)
	fda, err := ParseFDA(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fda.PrintInfo()

	if showInfo {
		return
	}

	if output == "" {
		base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
		output = filepath.Join(filepath.Dir(input), base+".wav")
	}

	fmt.Printf("\nDecoding with Relic Codec...\n")
	if err := DecodeFDAtoWAV(output, fda); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Done! Output: %s\n", output)
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

	fmt.Printf("Reading %s...\n", input)
	wav, err := ReadWAV(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("  SampleRate: %d Hz\n", wav.SampleRate)
	fmt.Printf("  Channels: %d\n", wav.Channels)
	fmt.Printf("  BitsPerSample: %d\n", wav.BitsPerSample)
	fmt.Printf("  Duration: %.1f seconds\n", float64(len(wav.PCMData))/(float64(wav.SampleRate)*float64(wav.Channels)*2))

	if wav.BitsPerSample != 16 {
		fmt.Fprintf(os.Stderr, "Error: only 16-bit WAV files are supported\n")
		os.Exit(1)
	}

	if output == "" {
		base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
		output = filepath.Join(filepath.Dir(input), base+".fda")
	}

	fmt.Printf("\nEncoding with Relic Codec (bitrate=%d)...\n", bitrate)
	if err := EncodeWAVtoFDA(output, wav, bitrate); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Done! Output: %s\n", output)
}

// Suppress unused import warning for math in wav2fda.go
var _ = filepath.Join
