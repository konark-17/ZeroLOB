# ⚡ Ultra-Low Latency Limit Order Book (LOB)

> **Sub-Microsecond, Zero-Allocation, Lock-Free Financial Matching Engine Engineered in Pure Go**

[![Go Version](https://img.shields.io/badge/Go-1.24%2B-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![Latency](https://img.shields.io/badge/Avg%20Latency-266%20ns-brightgreen?style=flat-square)](#benchmark-results)
[![Throughput](https://img.shields.io/badge/Throughput-2.46M%20ops%2Fsec-blue?style=flat-square)](#benchmark-results)
[![Allocations](https://img.shields.io/badge/Hot%20Path-0%20B%2Fop%20(0%20allocs)-orange?style=flat-square)](#zero-allocation-guarantee)
[![License](https://img.shields.io/badge/License-MIT-purple?style=flat-square)](LICENSE)

---

## 📌 Overview

In high-frequency trading (HFT) and quantitative exchange infrastructure, execution latency is measured in **nanoseconds**. While C++ is traditionally favored due to manual memory management, modern garbage-collected runtimes like Go can achieve **sub-microsecond execution** when engineered to actively bypass runtime overhead.

This project implements an exchange-grade, deterministic **Limit Order Book (LOB)** with price-time priority (FIFO). By combining **lock-free concurrency (LMAX Disruptor pattern)**, **cache-line padded atomics**, and a **custom contiguous slab allocator**, the hot path achieves **strictly zero allocations (`0 B/op`, `0 allocs/op`)**, completely immunizing the engine against Go Garbage Collector (GC) pauses.

---

## 🏗️ Architecture

```mermaid
flowchart LR
    subgraph Ingestion ["Feed Gateway (Producer Core)"]
        A[Inbound Orders / Market Data] -->|Non-Blocking Push| B[SPSC Ring Buffer]
    end

    subgraph Pipeline ["Lock-Free Concurrency Boundary"]
        B["Lock-Free SPSC Ring Buffer<br/>(Cache-Line Padded [56]byte)"]
    end

    subgraph CoreEngine ["Matching Core (Dedicated Consumer Core)"]
        B -->|Wait-Free Pop| C[OrderBook Matcher]
        C <-->|O(1) Alloc/Free| D["OrderArena<br/>(Contiguous Pre-Allocated Slab)"]
        C <-->|O(1) Price Lookup| E[Price Ladder Index]
        C -->|FIFO Match| F[Intrusive Double-Linked Queue]
    end

    subgraph Output ["Trade Broadcast Tape"]
        C -->|Zero-Alloc Slice Reuse| G[Execution Tape / Fills]
    end
```

---

## ⚡ Low-Latency Engineering Principles

### 1. Zero-Allocation Hot Path (`0 B/op`)
During order insertion, matching, and cancellation, the Go runtime heap is never touched.
- All order structs are allocated upfront in a contiguous memory block (`OrderArena`).
- Orders reference each other using **32-bit indices (`uint32`)** rather than 64-bit Go pointers (`*Order`). This slashes memory footprints by 50% and **prevents the Go GC from scanning millions of pointers**, eliminating stop-the-world pauses.

### 2. Lock-Free SPSC Ring Buffer (Disruptor Pattern)
- Inter-thread communication between the network gateway (producer) and the matching engine (consumer) uses a lock-free Single-Producer Single-Consumer circular queue.
- Powers-of-two capacity enables instant index calculation using bitwise operations: `slot = cursor & (capacity - 1)`.

### 3. CPU Cache Line False-Sharing Prevention
- On modern multi-core x86/ARM processors, CPU cores sync memory in 64-byte cache lines.
- The `head` (written by consumer) and `tail` (written by producer) atomic pointers are separated by **explicit 56-byte cache line padding (`[56]byte`)**. This guarantees that the producer and consumer cores never invalidate each other's L1/L2 caches.

### 4. $O(1)$ Order Cancellation & Amendments
- An open-addressing lookup table maps `OrderID -> ArenaIndex`. When a cancel command arrives, the engine unlinks the order from its price queue in $O(1)$ time without traversing the book.

---

## 📊 Benchmark Results

Benchmarked on an **Intel® Core™ i5-12450H** (Go 1.24):

### Throughput & Latency (1,000,000 Orders)
```text
==================================================================
 ⚡ ULTRA-LOW LATENCY LIMIT ORDER BOOK (LOB) BENCHMARK HARNESS
==================================================================
Total Orders:       1,000,000
Ring Buffer Size:   65,536 (Lock-Free SPSC)
Hot Path Goal:      0 B/op, 0 allocs/op, Sub-Microsecond p99
------------------------------------------------------------------
Elapsed Wall Time:  406.71 ms
Total Throughput:   2,458,716 orders/sec (~2.46 Million ops/sec)
------------------------------------------------------------------
Average Latency:    266.25 ns (0.266 µs)
p50 (Median):       < 100 ns
p90:                < 100 ns
p99:                < 100 ns
Max (Worst-Case):   1,624.80 µs (due to OS scheduler preemption)
==================================================================
```

### Zero-Allocation Verification (`go test -benchmem`)
```bash
$ go test -v -bench=BenchmarkLimitOrderHotPath -benchmem ./pkg/orderbook
BenchmarkLimitOrderHotPath-12    3799981    407.7 ns/op    0 B/op    0 allocs/op
PASS
```

---

## 📁 Repository Structure

```text
.
├── cmd/
│   ├── benchmark/
│   │   └── main.go           # 1M-order latency percentile & throughput suite
│   └── demo/
│       └── main.go           # Real-time ASCII order book depth & trade tape
├── pkg/
│   ├── orderbook/
│   │   ├── orderbook.go      # Price-time priority matching engine & ladder
│   │   └── orderbook_test.go # Unit tests & zero-alloc hot path benchmarks
│   ├── pool/
│   │   └── arena.go          # Pre-allocated contiguous Order slab allocator
│   ├── ringbuffer/
│   │   └── spsc.go           # Cache-padded, lock-free SPSC circular queue
│   └── types/
│       └── types.go          # Order, Trade, Side, and Command definitions
├── go.mod                    # Go module definition
└── README.md                 # Engineering documentation
```

---

## 🚀 Quickstart Guide

### Prerequisites
* **Go 1.22+** (tested on Go 1.24)

### 1. Run Unit Tests & Allocation Benchmarks
Verify zero memory allocations on the matching path:
```bash
go test -v -bench=. -benchmem ./pkg/orderbook
```

### 2. Run the 1,000,000 Order Latency Benchmark
Benchmark end-to-end throughput and tick-to-trade latency distributions:
```bash
go run ./cmd/benchmark/main.go
```

### 3. Run the Interactive Order Book Depth Visualizer
Simulate real-time resting orders, aggressive crossings, and cancellations with live ASCII ladder visualization:
```bash
go run ./cmd/demo/main.go
```

---

## 🔮 Future Roadmap

- [ ] **Kernel-Bypass Networking**: Ingest UDP multicast market data feeds via XDP / DPDK / `af_xdp`.
- [ ] **NASDAQ ITCH 5.0 Feed Handler**: Direct binary parsing of exchange PCAP feeds.
- [ ] **Core Pinning**: OS-level thread affinity (`pthread_setaffinity_np`) to lock the consumer to an isolated CPU core.
- [ ] **Market by Order (MBO) vs Market by Price (MBP)**: Dynamic level aggregation for market data publication.

