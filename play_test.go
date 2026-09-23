package main

import (
	"bytes"
	"io"
	"testing"
)

// pattern returns recognizable test PCM data where data[i] = byte(i).
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestNewChunkBufferSlicing(t *testing.T) {
	// 44100 Hz stereo: 2 bytes * 2 ch * 2205 samples = 8820 bytes per 50 ms.
	data := pattern(8820*3 + 100) // 3 full chunks + a partial one
	cb := newChunkBuffer(data, 44100, 2)

	if cb.chunkBytes != 8820 {
		t.Fatalf("chunkBytes = %d, want 8820", cb.chunkBytes)
	}
	if cb.numChunks != 4 {
		t.Fatalf("numChunks = %d, want 4", cb.numChunks)
	}
	if cb.totalBytes != len(data) {
		t.Fatalf("totalBytes = %d, want %d", cb.totalBytes, len(data))
	}
	if got := len(cb.chunks[3]); got != 100 {
		t.Fatalf("last chunk len = %d, want 100", got)
	}
	// Chunks must reassemble into the original data (zero-copy subslices).
	var joined []byte
	for i := 0; i < cb.numChunks; i++ {
		joined = append(joined, cb.chunks[i]...)
	}
	if !bytes.Equal(joined, data) {
		t.Fatal("chunks do not reassemble into the original data")
	}
}

func TestChunkSourceReadSequentialAndEOF(t *testing.T) {
	data := pattern(8820*2 + 500)
	cb := newChunkBuffer(data, 44100, 2)
	s := &chunkSource{cb: cb}

	var got []byte
	buf := make([]byte, 3000)
	for {
		n, err := s.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, want %d, or content mismatch", len(got), len(data))
	}
	if s.remaining() != 0 {
		t.Fatalf("remaining after EOF = %d, want 0", s.remaining())
	}
	// Reading past the end keeps returning EOF.
	if n, err := s.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("Read past EOF = (%d, %v), want (0, EOF)", n, err)
	}
}

func TestChunkSourceSeekSnapsToBoundary(t *testing.T) {
	data := pattern(8820 * 10)
	cb := newChunkBuffer(data, 44100, 2)
	s := &chunkSource{cb: cb}

	// Mid-chunk seek must snap down to the chunk grid.
	pos, err := s.Seek(8820*3+1234, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if pos != 8820*3 {
		t.Fatalf("Seek snapped to %d, want %d", pos, 8820*3)
	}
	if s.bytePos() != 8820*3 {
		t.Fatalf("bytePos = %d, want %d", s.bytePos(), 8820*3)
	}

	// Read after seek starts exactly at the snapped position.
	buf := make([]byte, 4)
	if _, err := s.Read(buf); err != nil {
		t.Fatalf("Read after seek: %v", err)
	}
	if !bytes.Equal(buf, data[8820*3:8820*3+4]) {
		t.Fatalf("read %v after seek, want %v", buf, data[8820*3:8820*3+4])
	}

	// Seek beyond the end lands at EOF.
	pos, err = s.Seek(1<<40, io.SeekStart)
	if err != nil || pos != int64(len(data)) {
		t.Fatalf("Seek past end = (%d, %v), want (%d, nil)", pos, err, len(data))
	}
	if s.remaining() != 0 {
		t.Fatalf("remaining at EOF = %d, want 0", s.remaining())
	}

	// Negative seek clamps to 0.
	if pos, err = s.Seek(-500, io.SeekStart); err != nil || pos != 0 {
		t.Fatalf("Seek(-500) = (%d, %v), want (0, nil)", pos, err)
	}

	// Relative and end-relative seeks.
	if pos, _ = s.Seek(8820*2, io.SeekStart); pos != 8820*2 {
		t.Fatalf("Seek to chunk 2 = %d", pos)
	}
	if pos, _ = s.Seek(8820, io.SeekCurrent); pos != 8820*3 {
		t.Fatalf("SeekCurrent = %d, want %d", pos, 8820*3)
	}
	if pos, _ = s.Seek(-8820, io.SeekEnd); pos != int64(8820*9) {
		t.Fatalf("SeekEnd = %d, want %d", pos, 8820*9)
	}

	// Invalid whence is rejected.
	if _, err = s.Seek(0, 42); err == nil {
		t.Fatal("invalid whence should return an error")
	}
}

func TestLayoutKey(t *testing.T) {
	cases := map[string]string{
		// Latin, any case (Shift/CapsLock must not matter)
		"q": "q", "Q": "q", "r": "r", "R": "r", "w": "w", "W": "w",
		"j": "j", "J": "j", "k": "k", "K": "k",
		// Cyrillic ЙЦУКЕН, any case
		"ц": "q", "Ц": "q", "к": "r", "К": "r", "у": "w", "У": "w",
		"о": "j", "О": "j", "л": "k", "Л": "k",
		// Non-letter keys must pass through unchanged
		" ": " ", "enter": "enter", "esc": "esc", "ctrl+c": "ctrl+c",
		"alt+right": "alt+right", "shift+left": "shift+left", "up": "up",
	}
	for in, want := range cases {
		if got := layoutKey(in); got != want {
			t.Errorf("layoutKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewChunkBufferOddRates(t *testing.T) {
	// 22050 Hz mono: samples per chunk must be a whole number of frames.
	cb := newChunkBuffer(pattern(1000), 22050, 1)
	if cb.chunkBytes%2 != 0 {
		t.Fatalf("chunkBytes = %d is not frame-aligned for mono 16-bit", cb.chunkBytes)
	}
	if want := (22050*chunkMillis + 500) / 1000 * 2; cb.chunkBytes != want {
		t.Fatalf("chunkBytes = %d, want %d", cb.chunkBytes, want)
	}

	// Stereo chunks must be aligned to 4 bytes (2 samples * 2 bytes).
	cb44 := newChunkBuffer(pattern(1000), 44100, 2)
	if cb44.chunkBytes%4 != 0 {
		t.Fatalf("chunkBytes = %d is not frame-aligned for stereo 16-bit", cb44.chunkBytes)
	}
}
