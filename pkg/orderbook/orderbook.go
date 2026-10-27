package orderbook

import (
	"errors"
	"lob-engine/pkg/pool"
	"lob-engine/pkg/types"
)

var (
	ErrOrderNotFound      = errors.New("order ID not found")
	ErrDuplicateOrderID   = errors.New("duplicate order ID")
	ErrInvalidQuantity    = errors.New("quantity must be greater than zero")
	ErrInvalidPrice       = errors.New("limit order price must be greater than zero")
	ErrPriceLevelExhaust  = errors.New("price level arena capacity exhausted")
)

// PriceLevel represents an aggregated price point in the ladder.
type PriceLevel struct {
	Price      uint64
	TotalQty   uint64
	OrderCount uint32
	Head       uint32 // Arena index of the oldest order at this price (FIFO head)
	Tail       uint32 // Arena index of the newest order at this price (FIFO tail)
	PoolIdx    uint32 // Direct slot index in the pre-allocated pool

	// Intrusive links to traverse active ladder levels
	Next *PriceLevel
	Prev *PriceLevel
}

// OrderBook is an ultra-low latency, zero-allocation Limit Order Book engine.
// All order nodes and price levels are managed via contiguous pre-allocated arenas.
type OrderBook struct {
	arena *pool.OrderArena

	// Fast O(1) order location mapping: OrderID -> ArenaSlotIndex
	orderIndexMap map[uint64]uint32

	// Price level lookup: Price -> *PriceLevel
	bidPriceMap map[uint64]*PriceLevel
	askPriceMap map[uint64]*PriceLevel

	// Pre-allocated slab of PriceLevel nodes to prevent runtime GC allocations
	levelPool     []PriceLevel
	levelFreeList []int
	levelFreeTop  int

	// Sorted doubly-linked list heads
	BestBid *PriceLevel // Highest buy price
	BestAsk *PriceLevel // Lowest sell price

	// Pre-allocated trade buffer to avoid slice allocations on match
	tradesBuffer []types.Trade
}

// NewOrderBook initializes an order book with pre-allocated memory pools.
func NewOrderBook(maxOrders uint32, maxPriceLevels int) *OrderBook {
	ob := &OrderBook{
		arena:         pool.NewOrderArena(maxOrders),
		orderIndexMap: make(map[uint64]uint32, maxOrders),
		bidPriceMap:   make(map[uint64]*PriceLevel, maxPriceLevels),
		askPriceMap:   make(map[uint64]*PriceLevel, maxPriceLevels),
		levelPool:     make([]PriceLevel, maxPriceLevels),
		levelFreeList: make([]int, maxPriceLevels),
		levelFreeTop:  maxPriceLevels,
		tradesBuffer:  make([]types.Trade, 0, 1024),
	}

	for i := 0; i < maxPriceLevels; i++ {
		ob.levelFreeList[maxPriceLevels-1-i] = i
	}

	return ob
}

// allocLevel acquires a PriceLevel node from the pre-allocated pool in O(1).
func (ob *OrderBook) allocLevel(price uint64) (*PriceLevel, error) {
	if ob.levelFreeTop == 0 {
		return nil, ErrPriceLevelExhaust
	}
	ob.levelFreeTop--
	idx := ob.levelFreeList[ob.levelFreeTop]
	lvl := &ob.levelPool[idx]
	lvl.Price = price
	lvl.TotalQty = 0
	lvl.OrderCount = 0
	lvl.Head = types.NilIndex
	lvl.Tail = types.NilIndex
	lvl.PoolIdx = uint32(idx)
	lvl.Next = nil
	lvl.Prev = nil
	return lvl, nil
}

// freeLevel returns a PriceLevel node to the pool in O(1).
func (ob *OrderBook) freeLevel(lvl *PriceLevel) {
	ob.levelFreeList[ob.levelFreeTop] = int(lvl.PoolIdx)
	ob.levelFreeTop++
}

// ProcessCommand executes an inbound OrderCommand and returns matches.
// Trades are appended to the caller's pre-allocated slice to maintain 0 B/op.
func (ob *OrderBook) ProcessCommand(cmd types.OrderCommand, outTrades *[]types.Trade) error {
	switch cmd.Type {
	case types.OrderLimit:
		return ob.processLimit(cmd, outTrades)
	case types.OrderMarket:
		return ob.processMarket(cmd, outTrades)
	case types.OrderCancel:
		return ob.CancelOrder(cmd.ID)
	default:
		return errors.New("unsupported order type")
	}
}

// processLimit executes or rests a Limit Order with Price-Time priority.
func (ob *OrderBook) processLimit(cmd types.OrderCommand, outTrades *[]types.Trade) error {
	if cmd.Qty == 0 {
		return ErrInvalidQuantity
	}
	if cmd.Price == 0 {
		return ErrInvalidPrice
	}
	if _, exists := ob.orderIndexMap[cmd.ID]; exists {
		return ErrDuplicateOrderID
	}

	remainingQty := cmd.Qty

	// 1. Match against opposite side
	if cmd.Side == types.SideBuy {
		// Buy crosses if BestAsk <= cmd.Price
		for ob.BestAsk != nil && ob.BestAsk.Price <= cmd.Price && remainingQty > 0 {
			remainingQty = ob.matchLevel(ob.BestAsk, cmd.ID, remainingQty, cmd.Timestamp, outTrades)
			if ob.BestAsk.OrderCount == 0 {
				ob.removeAskLevel(ob.BestAsk)
			}
		}
	} else {
		// Sell crosses if BestBid >= cmd.Price
		for ob.BestBid != nil && ob.BestBid.Price >= cmd.Price && remainingQty > 0 {
			remainingQty = ob.matchLevel(ob.BestBid, cmd.ID, remainingQty, cmd.Timestamp, outTrades)
			if ob.BestBid.OrderCount == 0 {
				ob.removeBidLevel(ob.BestBid)
			}
		}
	}

	// 2. Rest any unfilled quantity on the book
	if remainingQty > 0 {
		arenaIdx, orderNode, err := ob.arena.Alloc()
		if err != nil {
			return err
		}

		orderNode.ID = cmd.ID
		orderNode.Price = cmd.Price
		orderNode.Qty = cmd.Qty
		orderNode.FilledQty = cmd.Qty - remainingQty
		orderNode.Side = cmd.Side
		orderNode.Type = cmd.Type

		if cmd.Side == types.SideBuy {
			ob.insertBidOrder(arenaIdx, orderNode)
		} else {
			ob.insertAskOrder(arenaIdx, orderNode)
		}

		ob.orderIndexMap[cmd.ID] = arenaIdx
	}

	return nil
}

// processMarket executes a Market Order immediately against top of book.
func (ob *OrderBook) processMarket(cmd types.OrderCommand, outTrades *[]types.Trade) error {
	if cmd.Qty == 0 {
		return ErrInvalidQuantity
	}

	remainingQty := cmd.Qty

	if cmd.Side == types.SideBuy {
		for ob.BestAsk != nil && remainingQty > 0 {
			remainingQty = ob.matchLevel(ob.BestAsk, cmd.ID, remainingQty, cmd.Timestamp, outTrades)
			if ob.BestAsk.OrderCount == 0 {
				ob.removeAskLevel(ob.BestAsk)
			}
		}
	} else {
		for ob.BestBid != nil && remainingQty > 0 {
			remainingQty = ob.matchLevel(ob.BestBid, cmd.ID, remainingQty, cmd.Timestamp, outTrades)
			if ob.BestBid.OrderCount == 0 {
				ob.removeBidLevel(ob.BestBid)
			}
		}
	}

	return nil
}

// matchLevel walks orders FIFO at a price level, generating fills until exhausted or satisfied.
func (ob *OrderBook) matchLevel(lvl *PriceLevel, takerID uint64, takerQty uint32, timestamp uint64, outTrades *[]types.Trade) uint32 {
	currIdx := lvl.Head

	for currIdx != types.NilIndex && takerQty > 0 {
		maker := ob.arena.Get(currIdx)
		makerRem := maker.Qty - maker.FilledQty
		nextIdx := maker.Next

		matchQty := takerQty
		if makerRem < matchQty {
			matchQty = makerRem
		}

		maker.FilledQty += matchQty
		takerQty -= matchQty
		lvl.TotalQty -= uint64(matchQty)

		// Record trade in caller's pre-allocated slice
		if outTrades != nil {
			*outTrades = append(*outTrades, types.Trade{
				MakerOrderID: maker.ID,
				TakerOrderID: takerID,
				Price:        lvl.Price,
				Qty:          matchQty,
				Timestamp:    timestamp,
			})
		}

		// If maker order fully filled, unlink and reclaim memory
		if maker.FilledQty == maker.Qty {
			ob.unlinkOrderFromLevel(lvl, currIdx, maker)
			delete(ob.orderIndexMap, maker.ID)
			_ = ob.arena.Free(currIdx)
		}

		currIdx = nextIdx
	}

	return takerQty
}

// CancelOrder removes an active order from the book in O(1).
func (ob *OrderBook) CancelOrder(orderID uint64) error {
	arenaIdx, exists := ob.orderIndexMap[orderID]
	if !exists {
		return ErrOrderNotFound
	}

	order := ob.arena.Get(arenaIdx)
	if order == nil {
		return ErrOrderNotFound
	}

	var lvl *PriceLevel
	if order.Side == types.SideBuy {
		lvl = ob.bidPriceMap[order.Price]
	} else {
		lvl = ob.askPriceMap[order.Price]
	}

	if lvl != nil {
		unfilled := order.Qty - order.FilledQty
		lvl.TotalQty -= uint64(unfilled)
		ob.unlinkOrderFromLevel(lvl, arenaIdx, order)

		if lvl.OrderCount == 0 {
			if order.Side == types.SideBuy {
				ob.removeBidLevel(lvl)
			} else {
				ob.removeAskLevel(lvl)
			}
		}
	}

	delete(ob.orderIndexMap, orderID)
	return ob.arena.Free(arenaIdx)
}

// Helper: unlink order from a price level's intrusive doubly-linked list
func (ob *OrderBook) unlinkOrderFromLevel(lvl *PriceLevel, idx uint32, order *types.Order) {
	if order.Prev != types.NilIndex {
		ob.arena.Get(order.Prev).Next = order.Next
	} else {
		lvl.Head = order.Next
	}

	if order.Next != types.NilIndex {
		ob.arena.Get(order.Next).Prev = order.Prev
	} else {
		lvl.Tail = order.Prev
	}

	order.Prev = types.NilIndex
	order.Next = types.NilIndex
	lvl.OrderCount--
}

// insertBidOrder adds a buy order to the ladder with FIFO queue priority
func (ob *OrderBook) insertBidOrder(idx uint32, order *types.Order) {
	lvl, exists := ob.bidPriceMap[order.Price]
	if !exists {
		lvl, _ = ob.allocLevel(order.Price)
		ob.bidPriceMap[order.Price] = lvl
		ob.insertBidLevelInLadder(lvl)
	}

	ob.enqueueOrderInLevel(lvl, idx, order)
}

// insertAskOrder adds a sell order to the ladder with FIFO queue priority
func (ob *OrderBook) insertAskOrder(idx uint32, order *types.Order) {
	lvl, exists := ob.askPriceMap[order.Price]
	if !exists {
		lvl, _ = ob.allocLevel(order.Price)
		ob.askPriceMap[order.Price] = lvl
		ob.insertAskLevelInLadder(lvl)
	}

	ob.enqueueOrderInLevel(lvl, idx, order)
}

func (ob *OrderBook) enqueueOrderInLevel(lvl *PriceLevel, idx uint32, order *types.Order) {
	unfilled := order.Qty - order.FilledQty
	lvl.TotalQty += uint64(unfilled)
	lvl.OrderCount++

	if lvl.Tail == types.NilIndex {
		lvl.Head = idx
		lvl.Tail = idx
		order.Prev = types.NilIndex
		order.Next = types.NilIndex
	} else {
		tailOrder := ob.arena.Get(lvl.Tail)
		tailOrder.Next = idx
		order.Prev = lvl.Tail
		order.Next = types.NilIndex
		lvl.Tail = idx
	}
}

// insertBidLevelInLadder inserts a bid level in descending sorted order
func (ob *OrderBook) insertBidLevelInLadder(lvl *PriceLevel) {
	if ob.BestBid == nil {
		ob.BestBid = lvl
		return
	}

	if lvl.Price > ob.BestBid.Price {
		lvl.Next = ob.BestBid
		ob.BestBid.Prev = lvl
		ob.BestBid = lvl
		return
	}

	curr := ob.BestBid
	for curr.Next != nil && curr.Next.Price > lvl.Price {
		curr = curr.Next
	}

	lvl.Next = curr.Next
	lvl.Prev = curr
	if curr.Next != nil {
		curr.Next.Prev = lvl
	}
	curr.Next = lvl
}

// insertAskLevelInLadder inserts an ask level in ascending sorted order
func (ob *OrderBook) insertAskLevelInLadder(lvl *PriceLevel) {
	if ob.BestAsk == nil {
		ob.BestAsk = lvl
		return
	}

	if lvl.Price < ob.BestAsk.Price {
		lvl.Next = ob.BestAsk
		ob.BestAsk.Prev = lvl
		ob.BestAsk = lvl
		return
	}

	curr := ob.BestAsk
	for curr.Next != nil && curr.Next.Price < lvl.Price {
		curr = curr.Next
	}

	lvl.Next = curr.Next
	lvl.Prev = curr
	if curr.Next != nil {
		curr.Next.Prev = lvl
	}
	curr.Next = lvl
}

func (ob *OrderBook) removeBidLevel(lvl *PriceLevel) {
	if lvl.Prev != nil {
		lvl.Prev.Next = lvl.Next
	} else {
		ob.BestBid = lvl.Next
	}

	if lvl.Next != nil {
		lvl.Next.Prev = lvl.Prev
	}

	delete(ob.bidPriceMap, lvl.Price)
	ob.freeLevel(lvl)
}

func (ob *OrderBook) removeAskLevel(lvl *PriceLevel) {
	if lvl.Prev != nil {
		lvl.Prev.Next = lvl.Next
	} else {
		ob.BestAsk = lvl.Next
	}

	if lvl.Next != nil {
		lvl.Next.Prev = lvl.Prev
	}

	delete(ob.askPriceMap, lvl.Price)
	ob.freeLevel(lvl)
}
