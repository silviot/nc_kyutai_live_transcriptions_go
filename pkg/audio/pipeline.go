package audio

import (
	"fmt"
	"log/slog"
	"sync"
)

// Pipeline processes audio frames: resampling, format conversion, buffering
type Pipeline struct {
	inputSampleRate  int           // Input sample rate (usually 48000)
	outputSampleRate int           // Output sample rate (24000)
	inputFrameCh     <-chan []int16 // Input: raw int16 PCM
	outputFrameCh    chan []float32 // Output: resampled float32 PCM
	logger           *slog.Logger
	resampler        *Resampler
	mu               sync.Mutex
	closeCh          chan struct{}
	wg               sync.WaitGroup
}

// NewPipeline creates a new audio processing pipeline
func NewPipeline(inputSampleRate, outputSampleRate int, logger *slog.Logger) (*Pipeline, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Validate sample rates
	if inputSampleRate <= 0 || outputSampleRate <= 0 {
		return nil, fmt.Errorf("invalid sample rates: input=%d, output=%d", inputSampleRate, outputSampleRate)
	}

	if inputSampleRate == outputSampleRate {
		logger.Info("input and output sample rates are equal, no resampling needed",
			"sample_rate", inputSampleRate)
	}

	// Create resampler
	resampler, err := NewResampler(inputSampleRate, outputSampleRate, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create resampler: %w", err)
	}

	return &Pipeline{
		inputSampleRate:  inputSampleRate,
		outputSampleRate: outputSampleRate,
		outputFrameCh:    make(chan []float32, 10), // Bounded output channel
		logger:           logger,
		resampler:        resampler,
		closeCh:          make(chan struct{}),
	}, nil
}

// SetInputChannel sets the input frame channel
func (p *Pipeline) SetInputChannel(ch <-chan []int16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputFrameCh = ch
}

// Start begins processing audio frames (should be called in a goroutine)
func (p *Pipeline) Start() {
	p.wg.Add(1)
	go p.processLoop()
}

// processLoop reads input frames and processes them
func (p *Pipeline) processLoop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.closeCh:
			return
		default:
		}

		if p.inputFrameCh == nil {
			return
		}

		// Read from input channel
		frame, ok := <-p.inputFrameCh
		if !ok {
			p.logger.Debug("input frame channel closed")
			return
		}

		if len(frame) == 0 {
			continue
		}

		// Process frame
		processed, err := p.processFrame(frame)
		if err != nil {
			p.logger.Error("failed to process frame", "error", err)
			continue
		}

		// Send to output channel
		select {
		case p.outputFrameCh <- processed:
		case <-p.closeCh:
			return
		default:
			p.logger.Warn("output frame channel full, dropping frame")
		}
	}
}

// processFrame performs resampling and format conversion
func (p *Pipeline) processFrame(frame []int16) ([]float32, error) {
	// Convert int16 to float32
	float32Frame := p.int16ToFloat32(frame)

	// Resample if needed
	if p.inputSampleRate == p.outputSampleRate {
		return float32Frame, nil
	}

	resampled, err := p.resampler.Resample(float32Frame)
	if err != nil {
		return nil, fmt.Errorf("resampling failed: %w", err)
	}

	return resampled, nil
}

// int16ToFloat32 converts int16 PCM to float32 [-1.0, 1.0]
func (p *Pipeline) int16ToFloat32(samples []int16) []float32 {
	result := make([]float32, len(samples))
	for i, sample := range samples {
		// Normalize to [-1.0, 1.0]
		result[i] = float32(sample) / 32768.0
	}
	return result
}

// OutputChan returns the output frame channel
func (p *Pipeline) OutputChan() <-chan []float32 {
	return p.outputFrameCh
}

// ProcessFrame resamples a float32 audio frame (public API for direct processing)
func (p *Pipeline) ProcessFrame(frame []float32) ([]float32, error) {
	if len(frame) == 0 {
		return []float32{}, nil
	}

	// Resample if needed
	if p.inputSampleRate == p.outputSampleRate {
		return frame, nil
	}

	resampled, err := p.resampler.Resample(frame)
	if err != nil {
		return nil, fmt.Errorf("resampling failed: %w", err)
	}

	return resampled, nil
}

// Close closes the pipeline and cleans up
func (p *Pipeline) Close() error {
	close(p.closeCh)
	p.wg.Wait()
	return nil
}

// Resampler handles audio resampling from one sample rate to another
type Resampler struct {
	inputRate  int
	outputRate int
	ratio      float64
	logger     *slog.Logger
	buffer     []float32 // Buffer for partial frames
}

// NewResampler creates a new resampler
func NewResampler(inputRate, outputRate int, logger *slog.Logger) (*Resampler, error) {
	if logger == nil {
		logger = slog.Default()
	}

	return &Resampler{
		inputRate:  inputRate,
		outputRate: outputRate,
		ratio:      float64(outputRate) / float64(inputRate),
		logger:     logger,
		buffer:     make([]float32, 0),
	}, nil
}

// Resample performs simple linear interpolation resampling
// For production, consider libsoxr via cgo for higher quality
func (r *Resampler) Resample(input []float32) ([]float32, error) {
	if len(input) == 0 {
		return []float32{}, nil
	}

	// Calculate output size
	outputSize := int(float64(len(input)) * r.ratio)
	if outputSize == 0 {
		return []float32{}, nil
	}

	output := make([]float32, outputSize)

	// Simple linear interpolation
	for i := 0; i < outputSize; i++ {
		// Calculate position in input
		pos := float64(i) / r.ratio
		idx := int(pos)

		// Handle bounds
		if idx >= len(input)-1 {
			output[i] = input[len(input)-1]
			continue
		}

		// Linear interpolation
		frac := pos - float64(idx)
		output[i] = input[idx]*(1-float32(frac)) + input[idx+1]*float32(frac)
	}

	return output, nil
}

// ChunkBuffer buffers audio frames into fixed-size chunks
type ChunkBuffer struct {
	chunkSize int        // Samples per chunk
	buffer    []float32  // Accumulated samples
	logger    *slog.Logger
	mu        sync.Mutex
}

// NewChunkBuffer creates a new chunk buffer
func NewChunkBuffer(sampleRate, chunkDurationMs int, logger *slog.Logger) *ChunkBuffer {
	if logger == nil {
		logger = slog.Default()
	}

	// Calculate chunk size in samples
	// chunkDurationMs=200, sampleRate=24000 → 4800 samples
	chunkSize := (sampleRate * chunkDurationMs) / 1000

	return &ChunkBuffer{
		chunkSize: chunkSize,
		buffer:    make([]float32, 0, chunkSize),
		logger:    logger,
	}
}

// Add adds samples to the buffer and returns complete chunks
func (cb *ChunkBuffer) Add(samples []float32) [][]float32 {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.buffer = append(cb.buffer, samples...)

	var chunks [][]float32
	for len(cb.buffer) >= cb.chunkSize {
		chunk := make([]float32, cb.chunkSize)
		copy(chunk, cb.buffer[:cb.chunkSize])
		chunks = append(chunks, chunk)
		cb.buffer = cb.buffer[cb.chunkSize:]
	}

	return chunks
}

// Flush returns remaining samples as a partial chunk
func (cb *ChunkBuffer) Flush() []float32 {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if len(cb.buffer) == 0 {
		return []float32{}
	}

	chunk := make([]float32, len(cb.buffer))
	copy(chunk, cb.buffer)
	cb.buffer = cb.buffer[:0]

	return chunk
}

// Reset clears the buffer
func (cb *ChunkBuffer) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.buffer = cb.buffer[:0]
}
