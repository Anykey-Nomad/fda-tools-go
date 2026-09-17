package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

// WriteFDA writes an FDA file from PCM data
func WriteFDA(filename string, info *INFOChunk, channels int, data []byte) error {
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create FDA file: %w", err)
	}
	defer f.Close()

	// Write FDA Header
	hdr := FDAHeader{}
	copy(hdr.Signature[:], "Relic Chunky")
	hdr.SignServ = [4]byte{0x0d, 0x0a, 0x1a, 0x00}
	hdr.MajorVer = 1
	hdr.MinorVer = 1
	if err := binary.Write(f, binary.LittleEndian, &hdr); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Write FBIF chunk
	if err := writeFBIFChunk(f); err != nil {
		return fmt.Errorf("write FBIF: %w", err)
	}

	// Calculate FDA_ chunk size
	// FDA_ contains INFO + DATA chunks
	infoChunkSize := uint32(28) // szINFOChunk
	dataChunkSize := uint32(4) + uint32(len(data)) // szDATAChunk + data
	fdaChunkDataSize := infoChunkSize + dataChunkSize + 2*20 // INFO header + DATA header

	// Write FDA_ container chunk
	if err := writeFDAContainerChunk(f, fdaChunkDataSize); err != nil {
		return fmt.Errorf("write FDA_: %w", err)
	}

	// Write INFO chunk
	if err := writeINFOChunk(f, info); err != nil {
		return fmt.Errorf("write INFO: %w", err)
	}

	// Write DATA chunk
	if err := writeDATAChunk(f, data); err != nil {
		return fmt.Errorf("write DATA: %w", err)
	}

	return nil
}

func writeFDAChunkHeader(f *os.File, chunkType, chunkID string, chunkVer, chunkSize uint32, chunkName string) error {
	hdr := struct {
		ChunkType   [4]byte
		ChunkID     [4]byte
		ChunkVer    uint32
		ChunkSize   uint32
		LenChunkName uint32
	}{
		ChunkVer:  chunkVer,
		ChunkSize: chunkSize,
	}
	copy(hdr.ChunkType[:], chunkType)
	copy(hdr.ChunkID[:], chunkID)
	hdr.LenChunkName = uint32(len(chunkName))

	if err := binary.Write(f, binary.LittleEndian, &hdr); err != nil {
		return err
	}

	if len(chunkName) > 0 {
		if _, err := f.Write([]byte(chunkName)); err != nil {
			return err
		}
	}

	return nil
}

func writeFBIFChunk(f *os.File) error {
	pluginName := "RAW to FDA"
	userName := "fda2wav"
	burnTime := time.Now().Format("January 02, 2006, 3:04:05 PM")

	// Write chunk header
	if err := writeFDAChunkHeader(f, "DATA", "FBIF", 1, 0, "FileBurnInfo\x00"); err != nil {
		return err
	}

	// Write FBIF data
	// PluginName
	if err := binary.Write(f, binary.LittleEndian, uint32(len(pluginName))); err != nil {
		return err
	}
	if _, err := f.Write([]byte(pluginName)); err != nil {
		return err
	}
	// PluginVer
	if err := binary.Write(f, binary.LittleEndian, uint32(1)); err != nil {
		return err
	}
	// UserName
	if err := binary.Write(f, binary.LittleEndian, uint32(len(userName))); err != nil {
		return err
	}
	if _, err := f.Write([]byte(userName)); err != nil {
		return err
	}
	// BurnTime
	if err := binary.Write(f, binary.LittleEndian, uint32(len(burnTime))); err != nil {
		return err
	}
	if _, err := f.Write([]byte(burnTime)); err != nil {
		return err
	}

	return nil
}

func writeFDAContainerChunk(f *os.File, dataSize uint32) error {
	return writeFDAChunkHeader(f, "FOLD", "FDA ", 1, dataSize, "")
}

func writeINFOChunk(f *os.File, info *INFOChunk) error {
	// Write chunk header
	if err := writeFDAChunkHeader(f, "DATA", "INFO", 1, 28, ""); err != nil {
		return err
	}

	// Write INFO data (28 bytes)
	return binary.Write(f, binary.LittleEndian, info)
}

func writeDATAChunk(f *os.File, data []byte) error {
	// Write chunk header
	if err := writeFDAChunkHeader(f, "DATA", "DATA", 1, uint32(4+len(data)), ""); err != nil {
		return err
	}

	// Write data size
	if err := binary.Write(f, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}

	// Write audio data
	if _, err := f.Write(data); err != nil {
		return err
	}

	return nil
}
