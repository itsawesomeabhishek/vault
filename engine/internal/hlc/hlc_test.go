package hlc

import (
	"sync"
	"testing"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
)

func TestNowIsMonotonicWhenWallClockStalls(t *testing.T) {
	fixed := time.Unix(100, 0)
	c := NewWithSource("a", func() time.Time { return fixed })
	prev := c.Now()
	for range 100 {
		next := c.Now()
		if !Newer(next, prev) {
			t.Fatalf("version %v not newer than %v", next, prev)
		}
		prev = next
	}
}

func TestObserveOrdersLocalAfterRemote(t *testing.T) {
	c := NewWithSource("a", func() time.Time { return time.Unix(1, 0) })
	remote := &vaultpb.Version{Wall: time.Unix(50, 0).UnixNano(), Logical: 7, Node: "b"}
	c.Observe(remote)
	if v := c.Now(); !Newer(v, remote) {
		t.Fatalf("local %v should be newer than observed %v", v, remote)
	}
}

func TestCompareTieBreaksByNode(t *testing.T) {
	a := &vaultpb.Version{Wall: 1, Logical: 1, Node: "a"}
	b := &vaultpb.Version{Wall: 1, Logical: 1, Node: "b"}
	if Compare(a, b) != -1 || Compare(b, a) != 1 || Compare(a, a) != 0 {
		t.Fatal("unexpected tie-break ordering")
	}
	if Compare(nil, a) != -1 || Compare(a, nil) != 1 || Compare(nil, nil) != 0 {
		t.Fatal("nil must sort first")
	}
}

func TestConcurrentNowIsUnique(t *testing.T) {
	c := New("n")
	var mu sync.Mutex
	seen := map[[2]int64]bool{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				v := c.Now()
				k := [2]int64{v.Wall, int64(v.Logical)}
				mu.Lock()
				if seen[k] {
					t.Errorf("duplicate version %v", v)
				}
				seen[k] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}
