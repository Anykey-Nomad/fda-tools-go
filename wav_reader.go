package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WAVFile represents a parsed WAV file
type WAVFile struct {
	Format       WAVFmtData
	PCMData      []byte
	SampleRate   uint32
	Channels     uint16
	BitsPerSample uint16
}

// WAVFmtData is the format chunk data (without chunk header)
type WAVFmtData struct {
	AudioFormat   uint16
	NumChannels   uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
}

// ReadWAV reads a WAV file and returns parsed data
func ReadWAV(filename string) (*WAVFile, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Read RIFF header
	var riffHdr struct {
		ChunkID   [4]byte
		ChunkSize uint32
		Format    [4]byte
	}
	if err := binary.Read(f, binary.LittleEndian, &riffHdr); err != nil {
		return nil, fmt.Errorf("read RIFF header: %w", err)
	}
	if string(riffHdr.ChunkID[:]) != "RIFF" || string(riffHdr.Format[:]) != "WAVE" {
		return nil, fmt.Errorf("not a WAV file")
	}

	wav := &WAVFile{}

	// Read chunks
	for {
		var chunkHdr struct {
			ChunkID   [4]byte
			ChunkSize uint32
		}
		if err := binary.Read(f, binary.LittleEndian, &chunkHdr); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("read chunk header: %w", err)
		}

		chunkID := string(chunkHdr.ChunkID[:])

		switch chunkID {
		case "fmt ":
			// Read only the format data (16 bytes), not the chunk header
			if err := binary.Read(f, binary.LittleEndian, &wav.Format); err != nil {
				return nil, fmt.Errorf("read fmt chunk: %w", err)
			}
			wav.SampleRate = wav.Format.SampleRate
			wav.Channels = wav.Format.NumChannels
			wav.BitsPerSample = wav.Format.BitsPerSample
			// Skip any extra format bytes
			if chunkHdr.ChunkSize > 16 {
				io.CopyN(io.Discard, f, int64(chunkHdr.ChunkSize-16))
			}

		case "data":
			wav.PCMData = make([]byte, chunkHdr.ChunkSize)
			if _, err := io.ReadFull(f, wav.PCMData); err != nil {
				return nil, fmt.Errorf("read PCM data: %w", err)
			}

		default:
			// Skip unknown chunks
			if chunkHdr.ChunkSize > 0 {
				io.CopyN(io.Discard, f, int64(chunkHdr.ChunkSize))
			}
		}
	}

	if wav.PCMData == nil {
		return nil, fmt.Errorf("no PCM data found")
	}

	return wav, nil
}

// GetChannelSamples separates interleaved stereo samples into per-channel arrays
func (w *WAVFile) GetChannelSamples() [][]float32 {
	totalSamples := len(w.PCMData) / 2
	channels := int(w.Channels)
	framesPerChannel := totalSamples / channels

	channelsData := make([][]float32, channels)
	for ch := 0; ch < channels; ch++ {
		channelsData[ch] = make([]float32, framesPerChannel)
	}

	for i := 0; i < framesPerChannel; i++ {
		for ch := 0; ch < channels; ch++ {
			idx := (i*channels + ch) * 2
			if idx+1 < len(w.PCMData) {
				val := int16(w.PCMData[idx]) | int16(w.PCMData[idx+1])<<8
				channelsData[ch][i] = float32(val) / 32768.0
			}
		}
	}

	return channelsData
}
