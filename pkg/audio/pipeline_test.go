package audio

import (
	"log/slog"
	"testing"
)

func TestInt16ToFloat32Conversion(t *testing.T) {
	p, _ := NewPipeline(48000, 24000, slog.Default())
	defer p.Close()

	tests := []struct {
		name     string
		input    []int16
		expected []float32
	}{
		{
			name:     "zero",
			input:    []int16{0},
			expected: []float32{0.0},
		},
		{
			name:     "max positive",
			input:    []int16{32767},
			expected: []float32{0.999939},
		},
		{
			name:     "max negative",
			input:    []int16{-32768},
			expected: []float32{-1.0},
		},
		{
			name:     "mixed",
			input:    []int16{0, 16384, -16384},
			expected: []float32{0.0, 0.5, -0.5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.int16ToFloat32(tt.input)

			if len(result) != len(tt.expected) {
				t.Errorf("length mismatch: got %d, want %d", len(result), len(tt.expected))
				return
			}

			for i, val := range result {
				// Allow small floating-point error
				if abs(val-tt.expected[i]) > 0.0001 {
					t.Errorf("sample %d: got %f, want %f", i, val, tt.expected[i])
				}
			}
		})
	}
}

func TestResampling(t *testing.T) {
	resampler, err := NewResampler(48000, 24000, slog.Default())
	if err != nil {
		t.Fatalf("failed to create resampler: %v", err)
	}

	// Input: 4800 samples @ 48kHz (100ms)
	// Output: 2400 samples @ 24kHz (100ms)
	input := make([]float32, 4800)
	for i := range input {
		input[i] = 0.5 // Constant signal
	}

	output, err := resampler.Resample(input)
	if err != nil {
		t.Fatalf("resampling failed: %v", err)
	}

	expectedSize := 2400
	if len(output) != expectedSize {
		t.Errorf("output size mismatch: got %d, want %d", len(output), expectedSize)
	}

	// Verify all samples are approximately 0.5
	for i, val := range output {
		if abs(val-0.5) > 0.01 {
			t.Errorf("sample %d: got %f, want ~0.5", i, val)
		}
	}
}

func TestResamplingEmpty(t *testing.T) {
	resampler, _ := NewResampler(48000, 24000, slog.Default())

	output, err := resampler.Resample([]float32{})
	if err != nil {
		t.Fatalf("resampling empty failed: %v", err)
	}

	if len(output) != 0 {
		t.Errorf("expected empty output, got %d samples", len(output))
	}
}

func TestChunkBuffer(t *testing.T) {
	// 24kHz, 200ms chunks = 4800 samples
	cb := NewChunkBuffer(24000, 200, slog.Default())

	// Add 4800 samples → should get one chunk
	chunk1 := make([]float32, 4800)
	for i := range chunk1 {
		chunk1[i] = 0.1
	}

	chunks := cb.Add(chunk1)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}

	if len(chunks[0]) != 4800 {
		t.Errorf("chunk size mismatch: got %d, want 4800", len(chunks[0]))
	}

	// Add 2400 samples → should get no complete chunks
	chunk2 := make([]float32, 2400)
	chunks = cb.Add(chunk2)
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for partial, got %d", len(chunks))
	}

	// Add another 2400 samples → should get one chunk
	chunk3 := make([]float32, 2400)
	chunks = cb.Add(chunk3)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk from accumulated partial, got %d", len(chunks))
	}

	// Flush remaining (0 samples)
	remaining := cb.Flush()
	if len(remaining) != 0 {
		t.Errorf("expected empty flush, got %d samples", len(remaining))
	}
}

func TestChunkBufferFlush(t *testing.T) {
	cb := NewChunkBuffer(24000, 200, slog.Default())

	// Add partial chunk
	partial := make([]float32, 2400)
	cb.Add(partial)

	// Flush should return the partial
	flushed := cb.Flush()
	if len(flushed) != 2400 {
		t.Errorf("flush size mismatch: got %d, want 2400", len(flushed))
	}

	// Second flush should be empty
	flushed2 := cb.Flush()
	if len(flushed2) != 0 {
		t.Errorf("expected empty second flush, got %d samples", len(flushed2))
	}
}

func TestResamplingPreservesAmplitude(t *testing.T) {
	// Test that resampling a sine wave preserves amplitude in [-1, 1]
	resampler, _ := NewResampler(48000, 24000, slog.Default())

	// Generate 48kHz sine wave at 440Hz, 100ms
	input := make([]float32, 4800)
	for i := range input {
		// 440Hz sine wave
		phase := float64(i) / 48000.0 * 440.0 * 2 * 3.14159265
		input[i] = float32(0.8 * sinApprox(phase))
	}

	output, err := resampler.Resample(input)
	if err != nil {
		t.Fatal(err)
	}

	for i, v := range output {
		if v > 1.0 || v < -1.0 {
			t.Errorf("sample %d out of range: %f", i, v)
		}
	}
}

func TestResamplingManyFramesNoAmplification(t *testing.T) {
	// Simulate what happens in the real pipeline: many small frames through resampler
	resampler, _ := NewResampler(48000, 24000, slog.Default())
	cb := NewChunkBuffer(24000, 80, slog.Default()) // 80ms chunks = 1920 samples

	// Process 1000 frames of 120 samples each (like real Opus output)
	for frame := 0; frame < 1000; frame++ {
		input := make([]float32, 120) // 2.5ms at 48kHz
		for i := range input {
			// Simulate speech-like audio: sine with varying amplitude
			phase := float64(frame*120+i) / 48000.0 * 300.0 * 2 * 3.14159265
			input[i] = float32(0.3 * sinApprox(phase))
		}

		output, err := resampler.Resample(input)
		if err != nil {
			t.Fatalf("frame %d: resample failed: %v", frame, err)
		}

		// Check resampled values
		for i, v := range output {
			if v > 1.0 || v < -1.0 {
				t.Errorf("frame %d, sample %d: resampled value out of range: %f", frame, i, v)
				return
			}
		}

		// Feed through chunk buffer
		chunks := cb.Add(output)
		for _, chunk := range chunks {
			for i, v := range chunk {
				if v > 1.0 || v < -1.0 {
					t.Errorf("frame %d, chunk sample %d: value out of range: %f", frame, i, v)
					return
				}
			}
		}
	}
}

func TestProcessFrameAmplitude(t *testing.T) {
	// Test the full Pipeline.ProcessFrame path
	p, _ := NewPipeline(48000, 24000, slog.Default())
	defer p.Close()

	var maxSeen float32
	for frame := 0; frame < 500; frame++ {
		input := make([]float32, 480) // 10ms at 48kHz
		for i := range input {
			phase := float64(frame*480+i) / 48000.0 * 440.0 * 2 * 3.14159265
			input[i] = float32(0.9 * sinApprox(phase))
		}

		output, err := p.ProcessFrame(input)
		if err != nil {
			t.Fatalf("frame %d: %v", frame, err)
		}

		for _, v := range output {
			if v > maxSeen {
				maxSeen = v
			}
			if -v > maxSeen {
				maxSeen = -v
			}
			if v > 1.0 || v < -1.0 {
				t.Errorf("frame %d: value out of range: %f (max seen: %f)", frame, v, maxSeen)
				return
			}
		}
	}
	t.Logf("max absolute value seen across 500 frames: %f", maxSeen)
}

func sinApprox(x float64) float64 {
	// Use math-free sine approximation for test (avoid importing math in test)
	// Normalize to [0, 2*pi)
	const twoPi = 6.28318530718
	for x < 0 {
		x += twoPi
	}
	for x >= twoPi {
		x -= twoPi
	}
	// Bhaskara I approximation
	if x > 3.14159265 {
		x -= 3.14159265
		return -sinApprox0ToPi(x)
	}
	return sinApprox0ToPi(x)
}

func sinApprox0ToPi(x float64) float64 {
	// 16x(pi-x) / (5pi^2 - 4x(pi-x)) for x in [0, pi]
	const pi = 3.14159265
	num := 16 * x * (pi - x)
	den := 5*pi*pi - 4*x*(pi-x)
	if den == 0 {
		return 0
	}
	return num / den
}

func abs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
