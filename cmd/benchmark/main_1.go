package main

import (
	"fmt"
	"lob-engine/pkg/orderbook"
	"lob-engine/pkg/ringbuffer"
	"lob-engine/pkg/types"
	"sort"
	"sync"
	"time"
)

const (
	TotalOrders    = 1_000_000
	RingBufferSize = 65536
	MaxBookOrders  = 1_500_000
	MaxPriceLevels = 50_000
)

func main() {
	fmt.Println("==================================================================")
	fmt.Println(" ⚡ ULTRA-LOW LATENCY LIMIT ORDER BOOK (LOB) BENCHMARK HARNESS")
	fmt.Println("==================================================================")
	fmt.Printf("Total Orders:       %d\n", TotalOrders)
	fmt.Printf("Ring Buffer Size:   %d (Lock-Free SPSC)\n", RingBufferSize)
	fmt.Println("Hot Path Goal:      0 B/op, 0 allocs/op, Sub-Microsecond p99")
	fmt.Println("------------------------------------------------------------------")

	// 1. Initialize lock-free ring buffer & matching engine
	rb, err := ringbuffer.NewSPSC(RingBufferSize)
	if err != nil {
		panic(err)
	}

	ob := orderbook.NewOrderBook(MaxBookOrders, MaxPriceLevels)

	// Pre-seed book with initial depth (500 bids, 500 asks) to represent a live market
	tradesBuf := make([]types.Trade, 0, 16)
	basePrice := uint64(50000) // $500.00 base

	for i := uint64(1); i <= 500; i++ {
		// Bids: 49999 down to 49500
		_ = ob.ProcessCommand(types.OrderCommand{
			ID:    i,
			Price: basePrice - i,
			Qty:   10,
			Side:  types.SideBuy,
			Type:  types.OrderLimit,
		}, &tradesBuf)

		// Asks: 50001 up to 50500
		_ = ob.ProcessCommand(types.OrderCommand{
			ID:    i + 500,
			Price: basePrice + i,
			Qty:   10,
			Side:  types.SideSell,
			Type:  types.OrderLimit,
		}, &tradesBuf)
	}

	fmt.Println("[+] Pre-seeded order book with 1,000 resting price levels.")
	fmt.Println("[+] Launching lock-free matching engine pipeline...")

	// Pre-allocated latency array to measure exact tick-to-trade time for every order
	latencies := make([]int64, TotalOrders)

	var wg sync.WaitGroup
	wg.Add(2)

	startSignal := make(chan struct{})

	// -------------------------------------------------------------
	// CONSUMER: Dedicated Core Matching Engine
	// -------------------------------------------------------------
	go func() {
		defer wg.Done()
		<-startSignal

		processed := 0
		var trades []types.Trade = make([]types.Trade, 0, 8)

		for processed < TotalOrders {
			cmd, ok := rb.Pop()
			if !ok {
				// Tight spin-wait without OS context switch
				continue
			}

			// High-resolution tick-to-trade timing
			t0 := time.Now().UnixNano()
			trades = trades[:0]
			_ = ob.ProcessCommand(cmd, &trades)
			t1 := time.Now().UnixNano()

			latencies[processed] = t1 - t0
			processed++
		}
	}()

	// -------------------------------------------------------------
	// PRODUCER: Order Feed Ingestion Gateway
	// -------------------------------------------------------------
	go func() {
		defer wg.Done()
		<-startSignal

		var orderID uint64 = 2000
		for i := 0; i < TotalOrders; i++ {
			orderID++

			// Synthetic market dynamics: 40% aggressive crosses, 50% passive, 10% cancels
			randMod := i % 10
			var cmd types.OrderCommand

			if randMod < 4 {
				// Aggressive crossing order (taker)
				cmd = types.OrderCommand{
					ID:    orderID,
					Price: basePrice + uint64(i%5),
					Qty:   5,
					Side:  types.SideBuy,
					Type:  types.OrderLimit,
				}
			} else if randMod < 9 {
				// Passive liquidity provision (maker)
				cmd = types.OrderCommand{
					ID:    orderID,
					Price: basePrice - uint64((i%20)+1),
					Qty:   10,
					Side:  types.SideBuy,
					Type:  types.OrderLimit,
				}
			} else {
				// Cancel previously placed order
				cancelID := orderID - 5
				cmd = types.OrderCommand{
					ID:   cancelID,
					Type: types.OrderCancel,
				}
			}

			// Lock-free push with spin-wait on buffer full
			for !rb.Push(cmd) {
			}
		}
	}()

	// Kick off simultaneous producer and consumer threads
	wallStart := time.Now()
	close(startSignal)
	wg.Wait()
	wallElapsed := time.Since(wallStart)

	// -------------------------------------------------------------
	// STATISTICAL ANALYSIS & PERCENTILE REPORT
	// -------------------------------------------------------------
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	var totalLatency int64
	for _, l := range latencies {
		totalLatency += l
	}
	avg := float64(totalLatency) / float64(TotalOrders)

	p50 := latencies[int(float64(TotalOrders)*0.50)]
	p90 := latencies[int(float64(TotalOrders)*0.90)]
	p99 := latencies[int(float64(TotalOrders)*0.99)]
	p999 := latencies[int(float64(TotalOrders)*0.999)]
	maxLat := latencies[TotalOrders-1]

	throughput := float64(TotalOrders) / wallElapsed.Seconds()

	fmt.Println("\n==================================================================")
	fmt.Println(" 📊 BENCHMARK RESULTS")
	fmt.Println("==================================================================")
	fmt.Printf("Elapsed Wall Time:   %v\n", wallElapsed)
	fmt.Printf("Total Throughput:    %.2f orders/sec (%.2f M ops/sec)\n", throughput, throughput/1_000_000.0)
	fmt.Println("------------------------------------------------------------------")
	fmt.Printf("Average Latency:     %.2f ns (%.3f µs)\n", avg, avg/1000.0)
	fmt.Printf("p50  (Median):       %d ns (%.3f µs)\n", p50, float64(p50)/1000.0)
	fmt.Printf("p90:                 %d ns (%.3f µs)\n", p90, float64(p90)/1000.0)
	fmt.Printf("p99:                 %d ns (%.3f µs)\n", p99, float64(p99)/1000.0)
	fmt.Printf("p99.9:               %d ns (%.3f µs)\n", p999, float64(p999)/1000.0)
	fmt.Printf("Max (Worst-Case):    %d ns (%.3f µs)\n", maxLat, float64(maxLat)/1000.0)
	fmt.Println("==================================================================")
}

