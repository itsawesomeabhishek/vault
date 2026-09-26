package ring

import (
	"crypto/sha256"
	"math"
	"strconv"
	"testing"
)

func nodes(ids ...string) []Node {
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		n, _ := strconv.Atoi(id[1:])
		out = append(out, Node{ID: id, Zone: "z" + strconv.Itoa(n%2)})
	}
	return out
}

func TestPreferenceIsDeterministicAndComplete(t *testing.T) {
	a := New(nodes("n1", "n2", "n3", "n4"), 64)
	b := New(nodes("n4", "n3", "n2", "n1"), 64)
	h := sha256.Sum256([]byte("scans/key"))
	pa, pb := a.Preference(h), b.Preference(h)
	if len(pa) != 4 {
		t.Fatalf("preference length %d", len(pa))
	}
	for i := range pa {
		if pa[i] != pb[i] {
			t.Fatalf("order differs at %d: %v vs %v", i, pa, pb)
		}
	}
}

func TestOwnersSpanZones(t *testing.T) {
	r := New(nodes("n1", "n2", "n3", "n4"), 64)
	for i := range 1000 {
		h := sha256.Sum256([]byte(strconv.Itoa(i)))
		o := r.Owners(h, 3)
		zones := map[string]bool{}
		for _, n := range o {
			zones[n.Zone] = true
		}
		if len(zones) != 2 {
			t.Fatalf("owners %v do not span both zones", o)
		}
	}
}

func TestBalanced(t *testing.T) {
	r := New(nodes("n1", "n2", "n3", "n4", "n5", "n6"), DefaultVNodes)
	share := r.Share(1, 20000)
	for id, s := range share {
		if math.Abs(s-1.0/6) > 0.05 {
			t.Errorf("node %s owns %.3f of keys, want ~%.3f", id, s, 1.0/6)
		}
	}
}

func TestMinimalMovementOnJoin(t *testing.T) {
	before := New(nodes("n1", "n2", "n3", "n4"), DefaultVNodes)
	after := New(append(nodes("n1", "n2", "n3", "n4"), Node{ID: "n5", Zone: "z0"}), DefaultVNodes)
	moved := 0
	const total = 5000
	for i := range total {
		h := sha256.Sum256([]byte(strconv.Itoa(i)))
		if before.Owners(h, 1)[0] != after.Owners(h, 1)[0] {
			moved++
		}
	}
	if frac := float64(moved) / total; frac > 0.35 {
		t.Fatalf("%.2f of primaries moved on single join; consistent hashing should move ~1/5", frac)
	}
}

func TestEmptyRing(t *testing.T) {
	if p := New(nil, 8).Preference([32]byte{}); p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}
