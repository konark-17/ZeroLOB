# 🏛️ Technical Design Document: Ultra-Low Latency Limit Order Book (LOB)

**Author:** Konark  
**Field:** Information and Communication Technology (ICT)  
**System:** High-Frequency Trading (HFT) Matching Engine  
**Language:** Go 1.24  
**Classification:** Low-Latency Concurrent Systems  

---

## 1. Executive Summary & Problem Statement

In modern financial electronic markets, exchanges (e.g., NASDAQ, CME, LSE) and proprietary trading firms operate in a sub-microsecond latency environment where execution delays are measured in nanoseconds ($10^{-9}\,\text{s}$).

### The Core Dilemma in Garbage-Collected Languages
C++ is historically the default choice for low-latency systems because it has no runtime garbage collector (GC). Go is widely lauded for its concurrency primitives and memory safety, but standard Go programs suffer from:
1. **Garbage Collection Pauses**: When the Go runtime runs mark-and-sweep GC cycles, thread execution halts for milliseconds, introducing catastrophic p99 latency spikes (jitter).
2. **Pointer Indirection & Cache Thrashing**: Dynamically allocated pointers (`*Order`) force the CPU to chase scattered memory addresses across the L1/L2/L3 cache and RAM.
3. **Multi-Core False Sharing**: When independent CPU cores modify variables residing on the same 64-byte L1 cache line, the CPU hardware invalidates the line across cores, causing 10x–50x latency penalties.

### The Engineering Objective
To architect a deterministic, price-time priority (FIFO) **Limit Order Book (LOB)** in pure Go that matches and cancels orders with:
* **Strict Zero Allocations on the Hot Path (`0 B/op`, `0 allocs/op`)**.
* **Sub-Microsecond Latency** ($\approx 266\,\text{ns}$ average tick-to-trade).
* **Multi-Million Orders/sec Throughput** ($\approx 2.46\,\text{M ops/sec}$).
* **Total Immunity from GC Pauses**.

---

## 2. System Architecture & Data Flow

The engine implements a **single-writer architecture** (popularized by the LMAX Disruptor). Concurrency overhead (locks, mutexes, condition variables) is completely removed from the matching loop.

```mermaid
flowchart TD
    subgraph Ingestion ["Ingestion Gateway (Thread 1 - Core A)"]
        A[Market Data / Gateway Feed] -->|OrderCommand| B[Push to SPSC Ring Buffer]
    end

    subgraph MemoryBoundary ["Hardware Isolation Boundary"]
        B --> C["Lock-Free SPSC Ring Buffer<br/>(Cache-Padded 64-Byte Lines)"]
    end

    subgraph EngineCore ["Matching Engine Core (Thread 2 - Core B)"]
        C -->|Wait-Free Pop| D[OrderBook Matcher]
        D <-->|O(1) Slot Acquisition| E["OrderArena<br/>(Contiguous Pre-Allocated Slab)"]
        D <-->|O(1) Map Lookup| F[Order ID Index Map]
        D <-->|O(1) Best Level| G[Price Ladder Levels]
        G -->|FIFO Queues| H[Intrusive Double-Linked Orders]
    end

    subgraph OutputTape ["Trade Execution Tape"]
        D -->|Pre-Allocated Buffer| I[Executed Trades Stream]
    end
```

---

## 3. Deep Dive: Low-Latency Architectural Techniques

### 3.1. The Contiguous Memory Arena (`OrderArena`)
Instead of allowing Go's runtime allocator (`runtime.newobject`) to dynamically place orders on the heap, all order memory is pre-allocated in a contiguous array during engine boot:

```go
type OrderArena struct {
    nodes    []types.Order // Flat, contiguous array of orders
    freeList []uint32      // O(1) free index stack
    freeTop  int
    capacity uint32
}
```

#### Why Arena Allocation Outperforms the Heap:
1. **L1/L2 Cache Prefetching**: Memory is laid out sequentially. When the CPU accesses `nodes[i]`, hardware prefetchers load adjacent slots into the L1 cache before they are even requested.
2. **Preventing GC Root Scanning**: Because `types.Order` uses integer indices (`uint32`) rather than Go pointers (`*Order`), the Go GC ignores the entire arena during its scan phase. To the garbage collector, the arena is a flat block of scalar numbers.
3. **$O(1)$ Allocation & Deallocation**: Acquiring or releasing a slot is an index pop/push from `freeList`—executing in $< 5\,\text{ns}$.

---

### 3.2. Intrusive Doubly-Linked Queues via `uint32` Index Links
Each price level must maintain orders in strict **First-In, First-Out (FIFO)** time priority. Rather than wrapping orders in separate container nodes (like `std::list` in C++ or `container/list` in Go), the links are **intrusive** (embedded directly in the `Order` struct):

```go
type Order struct {
    ID        uint64
    Price     uint64
    Qty       uint32
    FilledQty uint32
    Side      Side
    Type      OrderType
    _pad      [2]byte // 8-byte alignment padding

    Prev uint32 // Index in OrderArena (not a 64-bit pointer)
    Next uint32 // Index in OrderArena (not a 64-bit pointer)
}
```

* **Memory Savings**: On a 64-bit OS, two pointers consume 16 bytes. Two `uint32` indices consume only 8 bytes.
* **Nil Representation**: `0xFFFFFFFF` (`types.NilIndex`) acts as the sentinel value analogous to `NULL`.

---

### 3.3. CPU Cache Line False Sharing & Padding
Modern CPUs exchange data between cores in **64-byte cache lines**.

When two variables reside on the same cache line:
* Core A (Producer) writing to `tail` invalidates the L1 cache of Core B (Consumer).
* Core B writing to `head` invalidates the L1 cache of Core A.
* The CPU enters a cache-coherency storm (**False Sharing**), stalling pipelines.

```
+------------------------ 64-Byte Cache Line ------------------------+
|  [56 bytes padding]  |  atomic.Uint64 tail (8 bytes)               |  <-- Core A Cache Line
+--------------------------------------------------------------------+
|  [56 bytes padding]  |  atomic.Uint64 head (8 bytes)               |  <-- Core B Cache Line
+--------------------------------------------------------------------+
```

#### The Code Implementation:
```go
type SPSC struct {
    _pad0    [56]byte
    tail     atomic.Uint64 // Written by Producer
    _pad1    [56]byte
    head     atomic.Uint64 // Written by Consumer
    _pad2    [56]byte
    capacity uint64
    mask     uint64
    buffer   []types.OrderCommand
}
```
Each pointer is guaranteed its own dedicated cache line, completely isolating core traffic.

---

### 3.4. Bitwise Power-of-Two Ring Buffer Masking
The SPSC ring buffer requires capacity to be a power of two ($2^n$, e.g., $65,536$).
In standard queues, wrapping around the end of a buffer requires the modulo operator (`%`):
$$\text{index} = \text{cursor} \pmod{\text{capacity}}$$
The CPU division instruction (`DIV` / `IDIV`) takes **15 to 40 CPU clock cycles**.

By constraining capacity to a power of two, modulo is replaced with a single-cycle bitwise AND (`AND`):
$$\text{index} = \text{cursor} \ \& \ (\text{capacity} - 1)$$
This executes in **1 clock cycle** ($\approx 0.3\,\text{ns}$).

---

## 4. Matching Logic & Order Lifecycle

The engine supports three fundamental order types:
1. **Limit Orders**: Rest on the book or cross immediately if price matches.
2. **Market Orders**: Sweep resting liquidity immediately across price levels.
3. **Cancel Orders**: Remove unexecuted resting volume in $O(1)$.

### Order Execution Matrix

| Action | Best Opposite Level Condition | Execution Path |
| :--- | :--- | :--- |
| **Buy Limit** | $\text{BestAsk} \le \text{Order.Price}$ | Matches aggressively against BestAsk FIFO queue until filled or price exceeds. Remainder rests as new Bid. |
| **Buy Limit** | $\text{BestAsk} > \text{Order.Price}$ (or No Asks) | Enters resting Bid ladder at `Order.Price` FIFO tail. |
| **Sell Limit** | $\text{BestBid} \ge \text{Order.Price}$ | Matches aggressively against BestBid FIFO queue until filled or price drops below. Remainder rests as new Ask. |
| **Sell Limit** | $\text{BestBid} < \text{Order.Price}$ (or No Bids) | Enters resting Ask ladder at `Order.Price` FIFO tail. |
| **Market Order** | Opposite side has volume | Sweeps levels from Top-of-Book downwards. Any unfilled remainder is discarded. |
| **Cancel Order** | Order ID in `orderIndexMap` | Direct $O(1)$ unlink from intrusive queue, arena slot reclaimed. |

---

## 5. Empirical Performance Verification

### 5.1. Hot Path Allocation Benchmark (`go test -benchmem`)
```bash
$ go test -v -bench=BenchmarkLimitOrderHotPath -benchmem ./pkg/orderbook
goos: windows
goarch: 386
pkg: lob-engine/pkg/orderbook
cpu: 12th Gen Intel(R) Core(TM) i5-12450H
BenchmarkLimitOrderHotPath-12    3799981    407.7 ns/op    0 B/op    0 allocs/op
PASS
```

### 5.2. 1,000,000 Order End-to-End Latency Benchmark
```text
==================================================================
Total Orders Processed:  1,000,000
Elapsed Wall Time:       406.71 ms
Total Throughput:        2,458,716 ops/sec (2.46 Million ops/sec)
------------------------------------------------------------------
Average Latency:         266.25 ns (0.266 µs)
Median (p50):            < 100 ns
90th Percentile (p90):   < 100 ns
99th Percentile (p99):   < 100 ns
99.9th Percentile:       < 100 ns
Max Latency:             1,624.80 µs (OS thread preemption quantum)
==================================================================
```

---

## 6. How to Present This Project in Technical Interviews

When explaining this project to interviewers at quantitative trading firms, networking companies, or systems engineering teams, emphasize these key discussion points:

### 1. "Why did you build this in Go instead of C++?"
> *"In C++, zero allocations and cache alignment are default expectations. In Go, achieving sub-microsecond latency requires mastering the Go runtime's internals. I had to design custom memory arenas and intrusive index queues to defeat the escape analyzer, force zero heap allocations (`0 B/op`), and eliminate all GC pause jitter."*

### 2. "How do you handle multi-threading without locks?"
> *"I adopted the LMAX Disruptor single-writer principle. Instead of protecting shared data structures with mutexes (which trigger expensive OS context switches and cache coherency invalidations), inbound orders pass through a lock-free Single-Producer Single-Consumer (SPSC) ring buffer. The matching engine core is single-threaded and pinned, processing orders sequentially in pure L1/L2 cache."*

### 3. "What is False Sharing and how did you prevent it?"
> *"CPU cores sync memory in 64-byte cache lines. If Core A's write pointer (`tail`) and Core B's read pointer (`head`) share a line, they constantly invalidate each other's L1 caches. I inserted explicit 56-byte cache-line padding arrays between atomic pointers, ensuring each atomic variable resides on its own private hardware cache line."*

### 4. "How is Order Cancellation $O(1)$?"
> *"Instead of scanning through price levels to find an order to cancel, I maintain a pre-allocated open-addressing index map that maps `OrderID` directly to its `ArenaIndex`. The order struct itself has embedded `Prev` and `Next` arena indices, allowing immediate $O(1)$ pointer unlinking from the doubly-linked list."*

---

## 7. Project Summary & Key Metrics

* **Codebase Size**: Modular, clean pure Go with zero third-party dependencies.
* **Throughput**: **$2.46\times 10^6$ orders/second**.
* **Latency**: **$266\,\text{ns}$ average tick-to-trade**.
* **Memory Efficiency**: **0 heap allocations** on the matching hot path.

