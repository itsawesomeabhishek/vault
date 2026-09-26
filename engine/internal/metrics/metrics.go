// Package metrics provides lock-free counters and latency histograms.
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var bounds = []time.Duration{
	time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
	25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond,
	500 * time.Millisecond, time.Second, 2500 * time.Millisecond, 5 * time.Second, 10 * time.Second, 30 * time.Second,
}

// Histogram records latencies in fixed exponential buckets.
type Histogram struct {
	counts [15]atomic.Uint64
	total  atomic.Uint64
	sumNs  atomic.Int64
}

// Observe records one duration.
func (h *Histogram) Observe(d time.Duration) {
	i := sort.Search(len(bounds), func(i int) bool { return d <= bounds[i] })
	h.counts[i].Add(1)
	h.total.Add(1)
	h.sumNs.Add(int64(d))
}

// Quantile returns the upper bound of the bucket containing quantile q.
func (h *Histogram) Quantile(q float64) time.Duration {
	total := h.total.Load()
	if total == 0 {
		return 0
	}
	target := uint64(q * float64(total))
	var seen uint64
	for i := range h.counts {
		seen += h.counts[i].Load()
		if seen > target || (seen == total) {
			if i < len(bounds) {
				return bounds[i]
			}
			return bounds[len(bounds)-1] * 2
		}
	}
	return bounds[len(bounds)-1] * 2
}

// Summary is a JSON-friendly view of a histogram.
type Summary struct {
	Count  uint64  `json:"count"`
	MeanMs float64 `json:"meanMs"`
	P50Ms  float64 `json:"p50Ms"`
	P99Ms  float64 `json:"p99Ms"`
}

// Summary snapshots the histogram.
func (h *Histogram) Summary() Summary {
	n := h.total.Load()
	s := Summary{Count: n, P50Ms: ms(h.Quantile(0.5)), P99Ms: ms(h.Quantile(0.99))}
	if n > 0 {
		s.MeanMs = float64(h.sumNs.Load()) / float64(n) / 1e6
	}
	return s
}

func ms(d time.Duration) float64 { return float64(d) / 1e6 }

// Registry groups named counters and histograms.
type Registry struct {
	mu       sync.Mutex
	counters map[string]*atomic.Uint64
	hists    map[string]*Histogram
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{counters: map[string]*atomic.Uint64{}, hists: map[string]*Histogram{}}
}

// Counter returns (creating) a named counter.
func (r *Registry) Counter(name string) *atomic.Uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[name]
	if !ok {
		c = &atomic.Uint64{}
		r.counters[name] = c
	}
	return c
}

// Histogram returns (creating) a named histogram.
func (r *Registry) Histogram(name string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.hists[name]
	if !ok {
		h = &Histogram{}
		r.hists[name] = h
	}
	return h
}

// Snapshot returns all current values.
func (r *Registry) Snapshot() (map[string]uint64, map[string]Summary) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := make(map[string]uint64, len(r.counters))
	for k, v := range r.counters {
		c[k] = v.Load()
	}
	h := make(map[string]Summary, len(r.hists))
	for k, v := range r.hists {
		h[k] = v.Summary()
	}
	return c, h
}
