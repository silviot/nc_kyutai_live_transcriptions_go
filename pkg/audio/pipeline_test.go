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

func abs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
