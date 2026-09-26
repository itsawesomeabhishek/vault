// Package ring implements consistent hashing with virtual nodes and
// zone-aware preference lists.
package ring

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"
)

// DefaultVNodes is the number of virtual nodes per physical node.
const DefaultVNodes = 128

// Node is a ring participant.
type Node struct {
	ID   string
	Zone string
}

type token struct {
	pos  uint64
	node int
}

// Ring is an immutable consistent-hash ring. Build a new one when membership
// changes.
type Ring struct {
	nodes  []Node
	tokens []token
}

// New builds a ring. Node order does not matter; the result is deterministic.
func New(nodes []Node, vnodes int) *Ring {
	if vnodes <= 0 {
		vnodes = DefaultVNodes
	}
	sorted := append([]Node(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	r := &Ring{nodes: sorted, tokens: make([]token, 0, len(sorted)*vnodes)}
	for i, n := range sorted {
		for v := range vnodes {
			sum := sha256.Sum256([]byte(n.ID + "#" + strconv.Itoa(v)))
			r.tokens = append(r.tokens, token{pos: binary.BigEndian.Uint64(sum[:8]), node: i})
		}
	}
	sort.Slice(r.tokens, func(i, j int) bool {
		if r.tokens[i].pos != r.tokens[j].pos {
			return r.tokens[i].pos < r.tokens[j].pos
		}
		return r.tokens[i].node < r.tokens[j].node
	})
	return r
}

// Len returns the number of physical nodes.
func (r *Ring) Len() int { return len(r.nodes) }

// Nodes returns the ring members sorted by ID.
func (r *Ring) Nodes() []Node { return append([]Node(nil), r.nodes...) }

// Preference returns every node ordered by preference for the given object
// hash. The walk starts at the object's position and proceeds clockwise; the
// order is then interleaved by zone so that any prefix of length n spreads
// across as many zones as possible.
func (r *Ring) Preference(objectHash [32]byte) []Node {
	if len(r.nodes) == 0 {
		return nil
	}
	pos := binary.BigEndian.Uint64(objectHash[:8])
	start := sort.Search(len(r.tokens), func(i int) bool { return r.tokens[i].pos >= pos })
	walk := make([]int, 0, len(r.nodes))
	seen := make([]bool, len(r.nodes))
	for i := 0; i < len(r.tokens) && len(walk) < len(r.nodes); i++ {
		t := r.tokens[(start+i)%len(r.tokens)]
		if !seen[t.node] {
			seen[t.node] = true
			walk = append(walk, t.node)
		}
	}
	out := make([]Node, 0, len(walk))
	picked := make([]bool, len(r.nodes))
	for len(out) < len(walk) {
		zones := map[string]bool{}
		for _, idx := range walk {
			n := r.nodes[idx]
			if picked[idx] || zones[n.Zone] {
				continue
			}
			zones[n.Zone] = true
			picked[idx] = true
			out = append(out, n)
		}
	}
	return out
}

// Owners returns the first n nodes of the preference list.
func (r *Ring) Owners(objectHash [32]byte, n int) []Node {
	p := r.Preference(objectHash)
	if n < len(p) {
		p = p[:n]
	}
	return p
}

// Share estimates the fraction of objects for which each node is one of the
// first n owners, using deterministic sampling.
func (r *Ring) Share(n, samples int) map[string]float64 {
	out := map[string]float64{}
	if len(r.nodes) == 0 || samples <= 0 {
		return out
	}
	for i := range samples {
		h := sha256.Sum256([]byte("sample-" + strconv.Itoa(i)))
		for _, node := range r.Owners(h, n) {
			out[node.ID]++
		}
	}
	for k := range out {
		out[k] /= float64(samples)
	}
	return out
}
