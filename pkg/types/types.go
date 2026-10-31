package types

// Side denotes Buy or Sell.
type Side uint8

const (
	SideBuy Side = 0
	SideSell Side = 1
)

func (s Side) String() string {
	if s == SideBuy {
		return "BUY"
	}
	return "SELL"
}

// OrderType denotes Limit, Market, or Cancel.
type OrderType uint8

const (
	OrderLimit  OrderType = 0
	OrderMarket OrderType = 1
	OrderCancel OrderType = 2
)

func (t OrderType) String() string {
	switch t {
	case OrderLimit:
		return "LIMIT"
	case OrderMarket:
		return "MARKET"
	case OrderCancel:
		return "CANCEL"
	default:
		return "UNKNOWN"
	}
}

// NilIndex represents an invalid index in arena pools (analogous to NULL pointer).
const NilIndex uint32 = 0xFFFFFFFF

// Order represents an active order in the book.
// Using uint32 indices instead of Go pointers (*Order) achieves:
// 1. Reduced struct size and denser cache packing.
// 2. ZERO Go GC root traversal (the entire arena is treated as scalar memory).
type Order struct {
	ID        uint64
	Price     uint64
	Qty       uint32
	FilledQty uint32
	Side      Side
	Type      OrderType
	_pad      [2]byte // 8-byte alignment padding

	// Intrusive doubly-linked list indices within the PriceLevel queue
	Prev uint32
	Next uint32
}

// Trade represents an executed match between a resting Maker order and an incoming Taker.
type Trade struct {
	MakerOrderID uint64
	TakerOrderID uint64
	Price        uint64
	Qty          uint32
	Timestamp    uint64
}

// OrderCommand represents an inbound message received via the lock-free ring buffer.
type OrderCommand struct {
	ID        uint64
	Price     uint64
	Qty       uint32
	Side      Side
	Type      OrderType
	_pad      [2]byte
	Timestamp uint64
}

