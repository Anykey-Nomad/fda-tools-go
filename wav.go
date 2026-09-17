package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// WAV constants
const (
	wavRIFFChunkID = "RIFF"
	wavRIFFFormat  = "WAVE"
	wavFmtChunkID  = "fmt "
	wavDataChunkID = "data"

	wavFormatPCM = 0x0001
)

// WAVHeader represents the RIFF/WAV file header
type WAVHeader struct {
	ChunkID   [4]byte // "RIFF"
	ChunkSize uint32
	Format    [4]byte // "WAVE"
}

// WAVFmtChunk represents the fmt sub-chunk
type WAVFmtChunk struct {
	ChunkID       [4]byte // "fmt "
	ChunkSize     uint32
	AudioFormat   uint16
	NumChannels   uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
}

// WAVDataChunk represents the data sub-chunk header
type WAVDataChunk struct {
	ChunkID   [4]byte // "data"
	ChunkSize uint32
}

// WriteWAV writes PCM audio data to a WAV file.
func WriteWAV(filename string, info *INFOChunk, data []byte) error {
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create WAV file: %w", err)
	}
	defer f.Close()

	// Compute sizes
	bytesPerSample := info.SampleSize / 8
	if bytesPerSample == 0 {
		bytesPerSample = 1
	}
	blockAlign := uint16(info.Channels * bytesPerSample)
	byteRate := info.SampleRate * uint32(blockAlign)

	// fmt chunk size (no extra params for PCM)
	fmtChunkDataSize := uint32(16)

	// RIFF chunk size = 4 (WAVE) + 8 + fmtChunkDataSize + 8 + dataSize
	dataSize := uint32(len(data))
	riffChunkSize := 4 + 8 + fmtChunkDataSize + 8 + dataSize

	// Write RIFF header
	hdr := WAVHeader{
		ChunkID:   [4]byte{'R', 'I', 'F', 'F'},
		ChunkSize: riffChunkSize,
		Format:    [4]byte{'W', 'A', 'V', 'E'},
	}
	if err := binary.Write(f, binary.LittleEndian, &hdr); err != nil {
		return fmt.Errorf("write RIFF header: %w", err)
	}

	// Write fmt sub-chunk
	fmtChunk := WAVFmtChunk{
		ChunkID:       [4]byte{'f', 'm', 't', ' '},
		ChunkSize:     fmtChunkDataSize,
		AudioFormat:   wavFormatPCM,
		NumChannels:   uint16(info.Channels),
		SampleRate:    info.SampleRate,
		ByteRate:      byteRate,
		BlockAlign:    blockAlign,
		BitsPerSample: uint16(info.SampleSize),
	}
	if err := binary.Write(f, binary.LittleEndian, &fmtChunk); err != nil {
		return fmt.Errorf("write fmt chunk: %w", err)
	}

	// Write data sub-chunk header
	dataChunkHdr := WAVDataChunk{
		ChunkID:   [4]byte{'d', 'a', 't', 'a'},
		ChunkSize: dataSize,
	}
	if err := binary.Write(f, binary.LittleEndian, &dataChunkHdr); err != nil {
		return fmt.Errorf("write data header: %w", err)
	}

	// Write audio data
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write audio data: %w", err)
	}

	return nil
}
