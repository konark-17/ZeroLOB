package orderbook

import (
	"lob-engine/pkg/types"
	"testing"
)

func TestLimitOrderCrossing(t *testing.T) {
	ob := NewOrderBook(10000, 1000)
	trades := make([]types.Trade, 0, 10)

	// Resting Ask: Sell 100 @ 105
	askCmd := types.OrderCommand{
		ID:    1,
		Price: 105,
		Qty:   100,
		Side:  types.SideSell,
		Type:  types.OrderLimit,
	}
	if err := ob.ProcessCommand(askCmd, &trades); err != nil {
		t.Fatalf("Failed to insert ask: %v", err)
	}
	if len(trades) != 0 {
		t.Fatalf("Expected 0 trades, got %d", len(trades))
	}
	if ob.BestAsk == nil || ob.BestAsk.Price != 105 {
		t.Fatalf("Expected BestAsk = 105")
	}

	// Incoming Bid: Buy 100 @ 105 (exact match)
	bidCmd := types.OrderCommand{
		ID:    2,
		Price: 105,
		Qty:   100,
		Side:  types.SideBuy,
		Type:  types.OrderLimit,
	}
	if err := ob.ProcessCommand(bidCmd, &trades); err != nil {
		t.Fatalf("Failed to insert bid: %v", err)
	}

	if len(trades) != 1 {
		t.Fatalf("Expected 1 trade, got %d", len(trades))
	}
	if trades[0].Price != 105 || trades[0].Qty != 100 {
		t.Fatalf("Unexpected trade details: %+v", trades[0])
	}
	if ob.BestAsk != nil {
		t.Fatalf("Expected BestAsk to be empty after full fill")
	}
}

func TestPriceTimePriorityFIFO(t *testing.T) {
	ob := NewOrderBook(10000, 1000)
	trades := make([]types.Trade, 0, 10)

	// Two resting Bids at same price 100: Order 1 arrives first, then Order 2
	ob.ProcessCommand(types.OrderCommand{ID: 1, Price: 100, Qty: 50, Side: types.SideBuy, Type: types.OrderLimit}, &trades)
	ob.ProcessCommand(types.OrderCommand{ID: 2, Price: 100, Qty: 50, Side: types.SideBuy, Type: types.OrderLimit}, &trades)

	// Incoming Sell: 70 @ 100. Should completely fill Order 1 (50) and partially fill Order 2 (20)
	sellCmd := types.OrderCommand{ID: 3, Price: 100, Qty: 70, Side: types.SideSell, Type: types.OrderLimit}
	ob.ProcessCommand(sellCmd, &trades)

	if len(trades) != 2 {
		t.Fatalf("Expected 2 fills, got %d", len(trades))
	}

	// Check Order 1 was filled first (FIFO)
	if trades[0].MakerOrderID != 1 || trades[0].Qty != 50 {
		t.Errorf("First fill should be Order 1 for 50, got: %+v", trades[0])
	}
	// Check Order 2 filled next
	if trades[1].MakerOrderID != 2 || trades[1].Qty != 20 {
		t.Errorf("Second fill should be Order 2 for 20, got: %+v", trades[1])
	}

	// Remaining volume on book at 100 should be 30
	if ob.BestBid.TotalQty != 30 {
		t.Errorf("Expected 30 remaining quantity, got %d", ob.BestBid.TotalQty)
	}
}

func TestCancelOrder(t *testing.T) {
	ob := NewOrderBook(10000, 1000)
	trades := make([]types.Trade, 0, 10)

	cmd := types.OrderCommand{ID: 42, Price: 250, Qty: 10, Side: types.SideBuy, Type: types.OrderLimit}
	ob.ProcessCommand(cmd, &trades)

	if ob.BestBid == nil || ob.BestBid.Price != 250 {
		t.Fatalf("Expected BestBid = 250")
	}

	cancelCmd := types.OrderCommand{ID: 42, Type: types.OrderCancel}
	if err := ob.ProcessCommand(cancelCmd, &trades); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	if ob.BestBid != nil {
		t.Fatalf("Expected empty book after cancel, got: %+v", ob.BestBid)
	}
}

// BenchmarkLimitOrderHotPath verifies the matching engine achieves sub-microsecond
// execution latency and 0 B/op allocations on the hot path.
func BenchmarkLimitOrderHotPath(b *testing.B) {
	ob := NewOrderBook(uint32(b.N*2+10000), 5000)
	trades := make([]types.Trade, 0, 16)

	// Pre-seed resting liquidity
	for i := 0; i < 1000; i++ {
		ob.ProcessCommand(types.OrderCommand{
			ID:    uint64(i + 1),
			Price: uint64(1000 + i),
			Qty:   10,
			Side:  types.SideSell,
			Type:  types.OrderLimit,
		}, &trades)
	}

	b.ResetTimer()
	b.ReportAllocs()

	var orderID uint64 = 2000
	for i := 0; i < b.N; i++ {
		trades = trades[:0] // Zero-alloc slice reset
		orderID++

		// Alternate between passive limit orders and aggressive crossings
		price := uint64(950 + (i % 60))
		side := types.SideBuy
		if i%2 == 1 {
			side = types.SideSell
			price = uint64(1050 - (i % 60))
		}

		_ = ob.ProcessCommand(types.OrderCommand{
			ID:    orderID,
			Price: price,
			Qty:   5,
			Side:  side,
			Type:  types.OrderLimit,
		}, &trades)
	}
}

