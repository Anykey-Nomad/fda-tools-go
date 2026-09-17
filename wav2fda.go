package main

import "fmt"

// EncodeWAVtoFDA encodes PCM WAV data to FDA format
func EncodeWAVtoFDA(filename string, wav *WAVFile, bitrate int) error {
	channels := int(wav.Channels)
	codecRate := int(wav.SampleRate)

	// Init encoder
	encoder := relicEncInit(channels, bitrate, codecRate)
	if encoder == nil {
		return fmt.Errorf("failed to init encoder (channels=%d, bitrate=%d, rate=%d)", channels, bitrate, codecRate)
	}

	// Get per-channel samples
	channelSamples := wav.GetChannelSamples()
	framesPerChannel := len(channelSamples[0]) / relicSamplesPerFrame

	fmt.Printf("  Frames per channel: %d\n", framesPerChannel)

	// Encode all frames
	allFrameData := make([]byte, 0, framesPerChannel*encoder.frameSize*channels)

	for i := 0; i < framesPerChannel; i++ {
		for ch := 0; ch < channels; ch++ {
			// Get 512 samples for this frame
			start := i * relicSamplesPerFrame
			end := start + relicSamplesPerFrame
			if end > len(channelSamples[ch]) {
				end = len(channelSamples[ch])
			}
			samples := channelSamples[ch][start:end]

			// Pad if needed
			if len(samples) < relicSamplesPerFrame {
				padded := make([]float32, relicSamplesPerFrame)
				copy(padded, samples)
				samples = padded
			}

			// Scale samples to match decoder's expected range
			// The decoder outputs values in the range [-32768, 32767]
			// So we need to scale our [-1, 1] input to that range
			scaledSamples := make([]float32, relicSamplesPerFrame)
			for j, s := range samples {
				scaledSamples[j] = s * 32768.0
			}

			// Apply forward DCT
			freq1 := make([]float32, relicMaxFreq)
			forwardDCT(scaledSamples, freq1, &encoder.dct, relicSizeHigh)

			// For stereo, use same data for both channels (mono copy)
			isMonoCopy := channels > 1

			// Quantize and pack
			frameData := quantizeAndPackFrame(freq1, freq1, &encoder.scales, encoder.freqSize, isMonoCopy)
			allFrameData = append(allFrameData, frameData[:encoder.frameSize]...)
		}

		if (i+1)%100 == 0 || i == framesPerChannel-1 {
			fmt.Printf("  Encoded %d/%d frames\r", i+1, framesPerChannel)
		}
	}
	fmt.Println()

	// Write FDA file
	info := &INFOChunk{
		Channels:     uint32(channels),
		SampleSize:   16,
		BlockBitrate: uint32(bitrate),
		SampleRate:   uint32(codecRate),
		BeginLoop:    0,
		EndLoop:      0xFFFFFFFF,
		StartOffset:  0,
	}

	return WriteFDA(filename, info, channels, allFrameData)
}

// clamp16Float clamps a float32 to int16 range
func clamp16Float(val float32) int16 {
	if val > 32767 {
		return 32767
	} else if val < -32768 {
		return -32768
	}
	return int16(val)
}

// suppress unused import

