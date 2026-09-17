package main

import (
	"fmt"
	"os"
)

// DecodeFDAtoWAV decodes Relic-compressed audio and writes a PCM WAV file
func DecodeFDAtoWAV(filename string, fda *FDAFile) error {
	// Init Relic decoder
	channels := int(fda.Info.Channels)
	bitrate := int(fda.Info.BlockBitrate)
	codecRate := int(fda.Info.SampleRate)

	decoder := relicInit(channels, bitrate, codecRate)
	if decoder == nil {
		return fmt.Errorf("failed to init Relic decoder (channels=%d, bitrate=%d, rate=%d)", channels, bitrate, codecRate)
	}
	defer relicFree(decoder)

	frameSize := relicGetFrameSize(decoder)
	if frameSize == 0 {
		return fmt.Errorf("invalid frame size")
	}

	// Calculate total samples
	// Frames are stored per-channel: [ch0_frame][ch1_frame][ch0_frame][ch1_frame]...
	totalBytes := len(fda.RawData)
	framesPerChannel := totalBytes / (frameSize * channels)
	totalSamples := framesPerChannel * relicSamplesPerFrame

	fmt.Printf("  Frames per channel: %d (frame_size=%d bytes, %d channels)\n", framesPerChannel, frameSize, channels)
	fmt.Printf("  Total samples: %d\n", totalSamples)

	// Decode all frames
	pcmData := make([]int16, 0, totalSamples*channels)
	chBuf := make([]byte, relicBufferSize)

	for i := 0; i < framesPerChannel; i++ {
		// Decode each channel from its own frame in the stream
		for ch := 0; ch < channels; ch++ {
			offset := (i*channels + ch) * frameSize
			// Copy frame data into buffer (padded to relicBufferSize)
			for j := 0; j < relicBufferSize; j++ {
				if j < frameSize && offset+j < totalBytes {
					chBuf[j] = fda.RawData[offset+j]
				} else {
					chBuf[j] = 0
				}
			}
			if !relicDecodeFrame(decoder, chBuf, ch) {
				fmt.Fprintf(os.Stderr, "  Warning: failed to decode frame %d channel %d\n", i, ch)
			}
		}

		// Extract PCM samples for all channels
		samples := make([]int16, relicSamplesPerFrame*channels)
		relicGetPCM16(decoder, samples)
		pcmData = append(pcmData, samples...)

		if (i+1)%100 == 0 || i == framesPerChannel-1 {
			fmt.Printf("  Decoded %d/%d frames\r", i+1, framesPerChannel)
		}
	}
	fmt.Println()

	// Write WAV file
	if err := WriteWAV(filename, &fda.Info, pcmDataToBytes(pcmData)); err != nil {
		return fmt.Errorf("write WAV: %w", err)
	}

	return nil
}

func relicFree(h *relicHandle) {
	// Go handles memory automatically; nothing to do here
	_ = h
}

func pcmDataToBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		data[i*2] = byte(s)
		data[i*2+1] = byte(s >> 8)
	}
	return data
}
