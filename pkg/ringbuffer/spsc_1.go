package ringbuffer

import (
	"errors"
	"lob-engine/pkg/types"
	"sync/atomic"
)

var (
	ErrBufferFull  = errors.New("ring buffer is full")
	ErrBufferEmpty = errors.New("ring buffer is empty")
)

// SPSC is a Single-Producer Single-Consumer lock-free circular ring buffer.
// Cache-line padding ([56]byte) is explicitly placed between atomic variables
// to prevent CPU L1/L2 False Sharing across multi-core processors.
type SPSC struct {
	// Padding to align to fresh 64-byte boundary
	_pad0 [56]byte

	// Tail is exclusively written by the Producer and read by Consumer
	tail atomic.Uint64

	// Padding ensures 'tail' and 'head' occupy distinct cache lines
	_pad1 [56]byte

	// Head is exclusively written by the Consumer and read by Producer
	head atomic.Uint64

	// Padding between indices and buffer metadata
	_pad2 [56]byte

	capacity uint64
	mask     uint64
	buffer   []types.OrderCommand
}

// NewSPSC creates a lock-free SPSC ring buffer.
// Capacity must be a power of two (e.g. 1024, 65536) for fast bitwise masking.
func NewSPSC(capacity uint64) (*SPSC, error) {
	if capacity == 0 || (capacity&(capacity-1)) != 0 {
		return nil, errors.New("capacity must be a power of two")
	}

	rb := &SPSC{
		capacity: capacity,
		mask:     capacity - 1,
		buffer:   make([]types.OrderCommand, capacity),
	}

	return rb, nil
}

// Push adds an OrderCommand to the ring buffer.
// Non-blocking and wait-free for the Producer. Returns false if the buffer is full.
func (rb *SPSC) Push(cmd types.OrderCommand) bool {
	currentTail := rb.tail.Load()
	currentHead := rb.head.Load()

	// Buffer is full when (tail - head) reaches capacity
	if currentTail-currentHead >= rb.capacity {
		return false
	}

	// Write slot via bitwise mask (faster than modulo)
	slot := currentTail & rb.mask
	rb.buffer[slot] = cmd

	// Publish write with store barrier
	rb.tail.Store(currentTail + 1)
	return true
}

// Pop retrieves an OrderCommand from the ring buffer.
// Non-blocking and wait-free for the Consumer. Returns false if the buffer is empty.
func (rb *SPSC) Pop() (types.OrderCommand, bool) {
	currentHead := rb.head.Load()
	currentTail := rb.tail.Load()

	// Buffer is empty when head reaches tail
	if currentHead >= currentTail {
		return types.OrderCommand{}, false
	}

	// Read slot via bitwise mask
	slot := currentHead & rb.mask
	cmd := rb.buffer[slot]

	// Publish read with store barrier
	rb.head.Store(currentHead + 1)
	return cmd, true
}

// Len returns the current number of queued elements in the buffer.
func (rb *SPSC) Len() uint64 {
	tail := rb.tail.Load()
	head := rb.head.Load()
	if tail >= head {
		return tail - head
	}
	return 0
}

// Capacity returns the total capacity of the ring buffer.
func (rb *SPSC) Capacity() uint64 {
	return rb.capacity
}

