package metrics

import (
	"testing"
	"time"
)

func TestHistogramQuantiles(t *testing.T) {
	h := &Histogram{}
	for i := 0; i < 100; i++ {
		h.Observe(10 * time.Millisecond)
	}
	h.Observe(2 * time.Second)
	s := h.Summary()
	if s.Count != 101 {
		t.Fatalf("count=%d", s.Count)
	}
	if s.P50Ms < 5 || s.P50Ms > 25 {
		t.Fatalf("p50=%v", s.P50Ms)
	}
	if s.P99Ms < 10 {
		t.Fatalf("p99=%v", s.P99Ms)
	}
}

func TestRegistrySnapshot(t *testing.T) {
	r := NewRegistry()
	r.Counter("puts").Add(3)
	r.Histogram("put_latency").Observe(5 * time.Millisecond)
	c, h := r.Snapshot()
	if c["puts"] != 3 {
		t.Fatalf("counters=%v", c)
	}
	if h["put_latency"].Count != 1 {
		t.Fatalf("hists=%v", h)
	}
}
