package core

import (
	"runtime"
	"sync"
)

// DefaultParallelism bounds fan-out for fetch / ingest / link work. It
// tracks GOMAXPROCS with a floor so single-core CI still overlaps the
// network-bound stages.
func DefaultParallelism() int {
	n := runtime.GOMAXPROCS(0)
	if n < 4 {
		return 4
	}
	return n
}

// HTTPConcurrency bounds in-flight registry requests. This is a network
// property, not a CPU one, so it is deliberately NOT derived from the core
// count: a fetch spends its time waiting on the registry (RTT), and Go's
// default transport negotiates HTTP/2 (no custom Dial/TLS is set on it), so
// those requests multiplex cheaply over ~one connection rather than one
// socket each. The useful ceiling is then RTT-hiding — a few dozen requests
// in flight drain the queue in a couple of round trips — bounded below the
// registry CDN's per-connection stream limit (~100); none of that scales
// with local cores. The CPU-bound consumer (hash + extract + link) has its
// own core-scaled knob, DefaultParallelism. 16 sits in that band: a fixed,
// principled default, not a user-tuned or core-derived value. (Measured:
// 16 / 32 / 64 are within network noise of each other for a cold install —
// HTTP/2 multiplexing keeps the connection busy at 16, so the registry and
// the pipe, not the request count, are the limit.)
const HTTPConcurrency = 16

// ForEachLimited runs fn over every item with at most limit goroutines
// in flight, and returns the first error reported by any invocation
// (others still run to completion). A limit < 1 falls back to
// DefaultParallelism.
func ForEachLimited[T any](items []T, limit int, fn func(T) error) error {
	if limit < 1 {
		limit = DefaultParallelism()
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for _, item := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func(it T) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(it); err != nil {
				once.Do(func() { firstErr = err })
			}
		}(item)
	}
	wg.Wait()
	return firstErr
}
