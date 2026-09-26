// Package hlc implements a hybrid logical clock. Versions produced by the
// clock are monotonic per node, track wall time closely, and are totally
// ordered across nodes (ties broken by node ID).
package hlc

import (
	"sync"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
)

// Clock issues versions for a single node. It is safe for concurrent use.
type Clock struct {
	mu      sync.Mutex
	node    string
	now     func() time.Time
	wall    int64
	logical uint32
}

// New returns a clock for the given node ID using the system time.
func New(node string) *Clock {
	return &Clock{node: node, now: time.Now}
}

// NewWithSource returns a clock driven by a custom time source (for tests).
func NewWithSource(node string, now func() time.Time) *Clock {
	return &Clock{node: node, now: now}
}

// Now returns a new version strictly greater than any version previously
// issued or observed by this clock.
func (c *Clock) Now() *vaultpb.Version {
	c.mu.Lock()
	defer c.mu.Unlock()
	pt := c.now().UnixNano()
	if pt > c.wall {
		c.wall = pt
		c.logical = 0
	} else {
		c.logical++
	}
	return &vaultpb.Version{Wall: c.wall, Logical: c.logical, Node: c.node}
}

// Observe merges a remote version so that later local versions order after it.
func (c *Clock) Observe(v *vaultpb.Version) {
	if v == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case v.Wall > c.wall:
		c.wall = v.Wall
		c.logical = v.Logical
	case v.Wall == c.wall && v.Logical > c.logical:
		c.logical = v.Logical
	}
}

// Compare returns -1, 0 or 1. A nil version sorts before every other version.
func Compare(a, b *vaultpb.Version) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case a.Wall != b.Wall:
		return cmp(a.Wall < b.Wall)
	case a.Logical != b.Logical:
		return cmp(a.Logical < b.Logical)
	case a.Node != b.Node:
		return cmp(a.Node < b.Node)
	default:
		return 0
	}
}

func cmp(less bool) int {
	if less {
		return -1
	}
	return 1
}

// Newer reports whether a is strictly newer than b.
func Newer(a, b *vaultpb.Version) bool { return Compare(a, b) > 0 }
