# hedge

[![Go Report Card](https://goreportcard.com/badge/github.com/bhope/hedge)](https://goreportcard.com/report/github.com/bhope/hedge) [![Go Reference](https://pkg.go.dev/badge/github.com/bhope/hedge.svg)](https://pkg.go.dev/github.com/bhope/hedge) ![Coverage](https://img.shields.io/badge/coverage-83%25-brightgreen) [![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

hedge is a Go library that reduces tail latency in fan-out architectures using adaptive hedged requests. It learns your service's latency distribution in real-time using DDSketch and fires backup requests only when the primary is genuinely slow - matching hand-tuned static thresholds with zero configuration. A token bucket budget prevents load amplification during outages.

---

## Table of Contents

- [Introduction](#introduction)
- [Evaluation](#evaluation)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [gRPC Support](#grpc-support)
- [How It Works](#how-it-works)
- [Why Not a Static Threshold?](#why-not-a-static-threshold)
- [References](#references)
- [Contributing](#contributing)
- [License](#license)

---

## Introduction

In fan-out architectures, a single user request fans out to dozens or hundreds of downstream services. Even when each service has only 1% slow responses, the probability that at least one backend is slow compounds dramatically - with 100 services, 63% of top-level requests will be delayed by at least one straggler. [Dean & Barroso (2013)](https://research.google/pubs/the-tail-at-scale/) identified hedged requests as the most effective mitigation: send a duplicate request to another server after a brief delay, and use whichever responds first.

hedge implements this with three components. A per-host [DDSketch](https://arxiv.org/abs/2004.08604) (Masson et al., VLDB 2019) tracks the latency distribution in real time with relative-error guarantees and constant memory, automatically adapting to load changes and deployments. When a request exceeds the estimated p90 latency, a backup is fired to the same target; whichever responds first wins and the loser is cancelled. A token bucket budget caps the hedge rate at a configurable percentage of total traffic, so that during genuine outages - when every request is slow - hedging stops before it doubles backend load.

**Features**

- Zero configuration - learns latency per target host automatically
- Drop-in `http.RoundTripper` and gRPC `UnaryClientInterceptor`
- Adaptive thresholds via DDSketch with relative-error guarantees
- Token bucket budget prevents load amplification during outages
- Constant memory, O(1) per request overhead (~35ns for sketch update)
- Full observability via `Stats` API

---

## Evaluation

![Evaluation](eval.png)

50,000 requests against a backend with lognormal base latency (mean=5ms) and 5% straggler probability (10× multiplier). Adaptive hedge matches the best hand-tuned static threshold at p99 (17.3ms vs 17.5ms) without requiring any manual configuration. Static thresholds either hedge too aggressively (10ms: 7.7% overhead) or too conservatively (50ms: p99 still at 54.9ms). In production where latency distributions shift with load and deployments, adaptive tracking avoids the stale-threshold problem entirely.

| Configuration    |  p50  |  p90  |   p95  |   p99  |  p999  | Overhead |
|------------------|-------|-------|--------|--------|--------|----------|
| No hedging       | 5.1ms | 9.0ms | 18.8ms | 65.0ms | 103.8ms |   0.0%  |
| Static 10ms      | 5.0ms | 9.0ms | 13.3ms | 17.5ms |  61.2ms |   7.7%  |
| Static 50ms      | 5.0ms | 9.0ms | 16.5ms | 54.9ms |  59.7ms |   2.1%  |
| Adaptive (hedge) | 5.0ms | 8.9ms | 12.3ms | 17.3ms |  63.5ms |   8.9%  |

Reproduce: `cd benchmark/simulate && go run .`

---

## Installation

```
go get github.com/bhope/hedge
```

---

## Quick Start

**Zero configuration** - the transport learns latency automatically:

```go
import "github.com/bhope/hedge"

client := &http.Client{
    Transport: hedge.New(http.DefaultTransport),
}
resp, err := client.Get("https://api.example.com/data")
```

**Tuned** - with explicit options and observability:

```go
var stats *hedge.Stats

client := &http.Client{
    Transport: hedge.New(http.DefaultTransport,
        hedge.WithPercentile(0.90),
        hedge.WithBudgetPercent(10),
        hedge.WithEstimatedRPS(1000),
        hedge.WithMinDelay(time.Millisecond),
        hedge.WithStats(&stats),
    ),
}

// After requests:
fmt.Printf("hedged=%d total=%d budget_exhausted=%d\n",
    stats.HedgedRequests.Load(),
    stats.TotalRequests.Load(),
    stats.BudgetExhausted.Load(),
)
```

---

## Configuration

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| WithPercentile(q) | float64 | 0.90 | Quantile of the latency distribution used as the hedge trigger threshold |
| WithMaxHedges(n) | int | 1 | Maximum number of in-flight hedge requests per call |
| WithBudgetPercent(p) | float64 | 10.0 | Max hedge rate as a percentage of estimated total traffic |
| WithEstimatedRPS(r) | float64 | 100 | Expected requests per second; scales the token bucket capacity |
| WithMinDelay(d) | time.Duration | 1ms | Floor on the hedge delay; prevents hedging on sub-millisecond latencies |
| WithStats(s) | **Stats | nil | Pointer to receive the live `Stats` struct for observability |

---

## gRPC Support

```go
import "github.com/bhope/hedge"

conn, err := grpc.NewClient(target,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    grpc.WithUnaryInterceptor(hedge.NewUnaryClientInterceptor(
        hedge.WithEstimatedRPS(500),
        hedge.WithBudgetPercent(10),
    )),
)
```

All options from the HTTP transport are supported. Per-target latency tracking uses `cc.Target()` as the host key.

---

## How It Works

### DDSketch

[DDSketch](https://arxiv.org/abs/2004.08604) is a streaming quantile sketch with relative-error guarantees: the returned quantile is always within ±ε of the true value (default ε=1%). hedge maintains one sketch per target host, updated on every completed request in O(1) time with constant memory. A tumbling window (default 30s) decays old observations so the sketch adapts to changing conditions.

### Adaptive Hedging

When a request exceeds the estimated p90 latency, a backup request is fired to the same target using a child context derived from the caller's context. Whichever response arrives first is returned to the caller; the other is cancelled and its response body drained to release the connection back to the pool. If the primary wins, it was just slow but not a straggler - no overhead is incurred.

### Hedging Budget

A token bucket limits hedge rate to a configurable percentage of total traffic (default 10%). The bucket refills at `estimatedRPS × budgetPercent / 100` tokens per second. When the bucket is empty, the request waits for the primary without firing a hedge. During genuine outages - when every request is slow and the bucket drains - hedging stops automatically, preventing the load-doubling spiral that would worsen the outage.

---

## Why Not a Static Threshold?

A static 10ms threshold looks great in benchmarks with fixed distributions. In production, latency shifts with load, deployments, GC pauses, and time of day - a threshold that is perfect at 3am causes 90%+ hedge rate at peak traffic. You would need to continuously monitor per-service latency and reconfigure thresholds as conditions change across every target your client talks to. Adaptive tracking handles this automatically: the sketch updates on every request, and the hedge threshold follows the actual distribution wherever it goes.

---

## References

- Jeffrey Dean and Luiz André Barroso. ["The Tail at Scale."](https://research.google/pubs/the-tail-at-scale/) *Communications of the ACM*, 56(2):74–80, 2013.
- Charles Masson, Jee E. Rim, and Homin K. Lee. ["DDSketch: A Fast and Fully-Mergeable Quantile Sketch with Relative-Error Guarantees."](https://arxiv.org/abs/2004.08604) *PVLDB*, 12(12):2195–2205, 2019.

---

## Contributing

Contributions are welcome! Please open an issue to discuss your idea before submitting a PR.

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

---

## License

hedge is released under the [MIT License](LICENSE).
