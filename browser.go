package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"
)

// browserMode displays an interactive file browser for FDA files in the
// current directory.  Arrow keys navigate, Enter plays the selected file,
// Q/Esc/Ctrl+C quits.  After playback returns, the browser resumes.
func browserMode() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get current directory: %v\n", err)
		os.Exit(1)
	}

	files := listFDAFiles(cwd)
	if len(files) == 0 {
		fmt.Println("No .fda files found in the current directory.")
		os.Exit(0)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot enable raw terminal: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(fd, oldState)

	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))

	selected := 0

	clearScreen := func() {
		if !stdoutTTY {
			return
		}
		fmt.Print("\033[2J\033[H") // clear + home
	}

	draw := func() {
		if !stdoutTTY {
			// Non-interactive: just print the list and exit.
			for _, f := range files {
				fmt.Printf("%s\n", f)
			}
			return
		}

		clearScreen()

		// Title
		fmt.Println("┌─── fda-tools-go ──── File Browser ──────────────────────────┐")
		fmt.Println("│                                                            │")

		// File list with cursor
		maxShow := 20
		if maxShow > len(files) {
			maxShow = len(files)
		}
		// Scroll window so selected item is always visible
		start := 0
		if selected >= maxShow {
			start = selected - maxShow + 1
		}

		for i := 0; i < maxShow; i++ {
			idx := start + i
			if idx >= len(files) {
				break
			}
			cursor := "  "
			if idx == selected {
				cursor = "▸ "
			}
			fmt.Printf("│  %s%s\n", cursor, files[idx])
		}
		// Pad remaining lines
		for _ = maxShow; maxShow < 20; maxShow++ {
			fmt.Println("│")
		}

		// Footer with controls
		fmt.Println("│                                                            │")
		fmt.Println("│  ↑↓ Navigate   Enter: Play   Q: Quit                      │")
		fmt.Println("└────────────────────────────────────────────────────────────┘")
		fmt.Println()

		// Show preview info for the selected file
		previewPath := filepath.Join(cwd, files[selected])
		if info, err := os.Stat(previewPath); err == nil {
			fmt.Printf("  Selected: %s  (%.1f KB)\n", files[selected], float64(info.Size())/1024.0)
		}
	}

	draw()

	// Key reading loop
	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 {
			if err != nil {
				break
			}
			continue
		}

		b := buf[0]

		// Escape sequence: ESC [ ...
		if b == 0x1b && n >= 3 && buf[1] == '[' {
			switch buf[2] {
			case 'A': // Up
				if selected > 0 {
					selected--
				}
			case 'B': // Down
				if selected < len(files)-1 {
					selected++
				}
			}
			draw()
			continue
		}

		switch b {
		case '\r', '\n': // Enter — play selected file
			playPath := filepath.Join(cwd, files[selected])
			// Restore terminal before playing
			term.Restore(fd, oldState)

			fmt.Printf("\n▶ Playing %s...\n", files[selected])
			if err := playFile(playPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}

			// Re-enter raw mode and redraw
			oldState2, err := term.MakeRaw(fd)
			if err != nil {
				return
			}
			oldState = oldState2
			draw()

		case 'q', 'Q':
			return

		case 3: // Ctrl+C
			fmt.Println()
			os.Exit(0)
		}
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
