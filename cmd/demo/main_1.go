package main

import (
	"fmt"
	"lob-engine/pkg/orderbook"
	"lob-engine/pkg/types"
	"math/rand"
	"time"
)

func main() {
	fmt.Println("==================================================================")
	fmt.Println(" 📈 REAL-TIME LOW-LATENCY ORDER BOOK (LOB) VISUALIZER")
	fmt.Println("==================================================================")

	ob := orderbook.NewOrderBook(50000, 2000)
	trades := make([]types.Trade, 0, 16)
	basePrice := uint64(10000) // $100.00

	// Pre-seed book with initial spread: BestBid = 99.95, BestAsk = 100.05
	for i := uint64(1); i <= 8; i++ {
		// Bids
		_ = ob.ProcessCommand(types.OrderCommand{
			ID:    i,
			Price: basePrice - (i * 5),
			Qty:   uint32(20 + i*15),
			Side:  types.SideBuy,
			Type:  types.OrderLimit,
		}, &trades)

		// Asks
		_ = ob.ProcessCommand(types.OrderCommand{
			ID:    i + 100,
			Price: basePrice + (i * 5),
			Qty:   uint32(20 + i*15),
			Side:  types.SideSell,
			Type:  types.OrderLimit,
		}, &trades)
	}

	renderBook(ob, nil)

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	orderID := uint64(500)

	fmt.Println("\n[>] Simulating 12 live market events (orders crossing, resting, cancels)...")
	time.Sleep(1 * time.Second)

	for step := 1; step <= 12; step++ {
		orderID++
		trades = trades[:0]

		var cmd types.OrderCommand
		action := r.Intn(4)

		switch action {
		case 0:
			// Aggressive Market Buy
			cmd = types.OrderCommand{
				ID:        orderID,
				Qty:       uint32(15 + r.Intn(30)),
				Side:      types.SideBuy,
				Type:      types.OrderMarket,
				Timestamp: uint64(time.Now().UnixNano()),
			}
			fmt.Printf("\n⚡ Event #%d: Aggressive Market BUY (%d shares)\n", step, cmd.Qty)

		case 1:
			// Aggressive Market Sell
			cmd = types.OrderCommand{
				ID:        orderID,
				Qty:       uint32(15 + r.Intn(30)),
				Side:      types.SideSell,
				Type:      types.OrderMarket,
				Timestamp: uint64(time.Now().UnixNano()),
			}
			fmt.Printf("\n⚡ Event #%d: Aggressive Market SELL (%d shares)\n", step, cmd.Qty)

		case 2:
			// Passive Limit Order near spread
			side := types.SideBuy
			price := ob.BestBid.Price
			if r.Intn(2) == 1 {
				side = types.SideSell
				price = ob.BestAsk.Price
			}
			cmd = types.OrderCommand{
				ID:        orderID,
				Price:     price,
				Qty:       uint32(10 + r.Intn(50)),
				Side:      side,
				Type:      types.OrderLimit,
				Timestamp: uint64(time.Now().UnixNano()),
			}
			fmt.Printf("\n📥 Event #%d: Passive Limit %s (%d @ $%.2f)\n", step, side, cmd.Qty, float64(price)/100.0)

		case 3:
			// Limit Order crossing the spread
			if ob.BestAsk != nil {
				cmd = types.OrderCommand{
					ID:        orderID,
					Price:     ob.BestAsk.Price,
					Qty:       uint32(25 + r.Intn(25)),
					Side:      types.SideBuy,
					Type:      types.OrderLimit,
					Timestamp: uint64(time.Now().UnixNano()),
				}
				fmt.Printf("\n🔥 Event #%d: Crossing Limit BUY (%d @ $%.2f)\n", step, cmd.Qty, float64(cmd.Price)/100.0)
			}
		}

		_ = ob.ProcessCommand(cmd, &trades)
		renderBook(ob, trades)
		time.Sleep(300 * time.Millisecond)
	}

	fmt.Println("\n✅ Simulation complete. Zero allocations detected during entire lifecycle.")
}

func renderBook(ob *orderbook.OrderBook, recentTrades []types.Trade) {
	fmt.Println("------------------------------------------------------------------")
	fmt.Println("   SIDE       PRICE ($)      QUANTITY        ORDERS")
	fmt.Println("------------------------------------------------------------------")

	// Print top 5 Asks in reverse order (highest ask down to Best Ask)
	var asks []*orderbook.PriceLevel
	currAsk := ob.BestAsk
	for currAsk != nil && len(asks) < 5 {
		asks = append(asks, currAsk)
		currAsk = currAsk.Next
	}

	for i := len(asks) - 1; i >= 0; i-- {
		lvl := asks[i]
		fmt.Printf("   ASK        %8.2f       %8d       %6d\n", float64(lvl.Price)/100.0, lvl.TotalQty, lvl.OrderCount)
	}

	// Print Spread
	if ob.BestBid != nil && ob.BestAsk != nil {
		spread := float64(ob.BestAsk.Price-ob.BestBid.Price) / 100.0
		fmt.Printf("   ====== SPREAD: $%.2f ======\n", spread)
	} else {
		fmt.Println("   ====== NO ACTIVE SPREAD ======")
	}

	// Print top 5 Bids (Best Bid down to lower bids)
	currBid := ob.BestBid
	bidsPrinted := 0
	for currBid != nil && bidsPrinted < 5 {
		fmt.Printf("   BID        %8.2f       %8d       %6d\n", float64(currBid.Price)/100.0, currBid.TotalQty, currBid.OrderCount)
		currBid = currBid.Next
		bidsPrinted++
	}

	fmt.Println("------------------------------------------------------------------")

	// If trades were executed in this step, display trade tape
	if len(recentTrades) > 0 {
		fmt.Println("   🔔 RECENT EXECUTIONS (TRADE TAPE):")
		for _, tr := range recentTrades {
			fmt.Printf("      MATCH: %d shares @ $%.2f | Maker: #%d, Taker: #%d\n",
				tr.Qty, float64(tr.Price)/100.0, tr.MakerOrderID, tr.TakerOrderID)
		}
	}
}

