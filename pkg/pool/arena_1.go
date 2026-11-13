package pool

import (
	"errors"
	"lob-engine/pkg/types"
)

var (
	ErrArenaExhausted = errors.New("arena pool capacity exhausted")
	ErrInvalidIndex   = errors.New("invalid arena slot index")
)

// OrderArena is a contiguous pre-allocated slab allocator for Order structures.
// Memory is allocated upfront during engine boot to guarantee 0 heap allocations
// on the matching hot path.
type OrderArena struct {
	nodes    []types.Order
	freeList []uint32
	freeTop  int
	capacity uint32
}

// NewOrderArena pre-allocates contiguous memory for maximum orders.
func NewOrderArena(capacity uint32) *OrderArena {
	arena := &OrderArena{
		nodes:    make([]types.Order, capacity),
		freeList: make([]uint32, capacity),
		freeTop:  int(capacity),
		capacity: capacity,
	}

	// Initialize free list in reverse order (0 is allocated first)
	for i := uint32(0); i < capacity; i++ {
		arena.freeList[capacity-1-i] = i
	}

	return arena
}

// Alloc returns an unused Order slot from the pre-allocated pool in O(1).
func (a *OrderArena) Alloc() (uint32, *types.Order, error) {
	if a.freeTop == 0 {
		return types.NilIndex, nil, ErrArenaExhausted
	}

	a.freeTop--
	idx := a.freeList[a.freeTop]
	node := &a.nodes[idx]

	// Reset node state
	node.ID = 0
	node.Price = 0
	node.Qty = 0
	node.FilledQty = 0
	node.Side = types.SideBuy
	node.Type = types.OrderLimit
	node.Prev = types.NilIndex
	node.Next = types.NilIndex

	return idx, node, nil
}

// Free returns an Order slot back to the arena in O(1).
func (a *OrderArena) Free(idx uint32) error {
	if idx >= a.capacity {
		return ErrInvalidIndex
	}

	a.freeList[a.freeTop] = idx
	a.freeTop++
	return nil
}

// Get returns direct pointer access to the order at the specified arena index in O(1).
func (a *OrderArena) Get(idx uint32) *types.Order {
	if idx >= a.capacity {
		return nil
	}
	return &a.nodes[idx]
}

// InUse returns the count of currently allocated orders.
func (a *OrderArena) InUse() uint32 {
	return a.capacity - uint32(a.freeTop)
}

// Capacity returns the maximum pool capacity.
func (a *OrderArena) Capacity() uint32 {
	return a.capacity
}

