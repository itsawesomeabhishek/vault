package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/membership"
)

type pair struct{ a, b string }

// Faults holds injected network faults: blocked links (partitions), downed
// nodes and per-node latency. It is used by chaos tests and the chaos API.
type Faults struct {
	mu      sync.RWMutex
	blocked map[pair]bool
	down    map[string]bool
	delay   map[string]time.Duration
}

// NewFaults returns an empty fault set.
func NewFaults() *Faults {
	return &Faults{blocked: map[pair]bool{}, down: map[string]bool{}, delay: map[string]time.Duration{}}
}

func orderedPair(a, b string) pair {
	if a > b {
		a, b = b, a
	}
	return pair{a, b}
}

// Block cuts the link between a and b in both directions.
func (f *Faults) Block(a, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked[orderedPair(a, b)] = true
}

// Unblock restores the link between a and b.
func (f *Faults) Unblock(a, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blocked, orderedPair(a, b))
}

// Partition blocks every link between the two groups.
func (f *Faults) Partition(left, right []string) {
	for _, a := range left {
		for _, b := range right {
			f.Block(a, b)
		}
	}
}

// Heal removes every partition, downed node and delay.
func (f *Faults) Heal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked = map[pair]bool{}
	f.down = map[string]bool{}
	f.delay = map[string]time.Duration{}
}

// SetDown makes a node unreachable (simulated crash) or reachable again.
func (f *Faults) SetDown(node string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if down {
		f.down[node] = true
	} else {
		delete(f.down, node)
	}
}

// SetDelay adds latency to every request served by node.
func (f *Faults) SetDelay(node string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d <= 0 {
		delete(f.delay, node)
	} else {
		f.delay[node] = d
	}
}

// Blocked lists nodes the given node cannot reach.
func (f *Faults) Blocked(node string) []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var out []string
	for p := range f.blocked {
		switch node {
		case p.a:
			out = append(out, p.b)
		case p.b:
			out = append(out, p.a)
		}
	}
	return out
}

// Check returns ErrUnavailable if from cannot reach to, after applying any
// injected delay.
func (f *Faults) Check(ctx context.Context, from, to string) error {
	f.mu.RLock()
	blocked := f.blocked[orderedPair(from, to)] || f.down[to] || f.down[from]
	d := f.delay[to]
	f.mu.RUnlock()
	if blocked {
		return fmt.Errorf("%w: link %s -> %s is down", ErrUnavailable, from, to)
	}
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		}
	}
	return nil
}

// FaultyDialer wraps a Dialer so that every call first consults Faults.
type FaultyDialer struct {
	Inner  Dialer
	Self   string
	Faults *Faults
}

// Dial implements Dialer.
func (d FaultyDialer) Dial(m membership.Member) (Peer, error) {
	p, err := d.Inner.Dial(m)
	if err != nil {
		return nil, err
	}
	return &faultyPeer{inner: p, from: d.Self, to: m.ID, f: d.Faults}, nil
}

type faultyPeer struct {
	inner    Peer
	from, to string
	f        *Faults
}

func (p *faultyPeer) check(ctx context.Context) error { return p.f.Check(ctx, p.from, p.to) }

func (p *faultyPeer) Ping(ctx context.Context) (string, error) {
	if err := p.check(ctx); err != nil {
		return "", err
	}
	return p.inner.Ping(ctx)
}

func (p *faultyPeer) PutShard(ctx context.Context, hash string, data []byte) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.inner.PutShard(ctx, hash, data)
}

func (p *faultyPeer) GetShard(ctx context.Context, hash string) ([]byte, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.GetShard(ctx, hash)
}

func (p *faultyPeer) HasShards(ctx context.Context, hashes []string, verify bool) ([]bool, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.HasShards(ctx, hashes, verify)
}

func (p *faultyPeer) PutManifest(ctx context.Context, m *vaultpb.Manifest, hintFor string) (bool, error) {
	if err := p.check(ctx); err != nil {
		return false, err
	}
	return p.inner.PutManifest(ctx, m, hintFor)
}

func (p *faultyPeer) GetManifest(ctx context.Context, bucket, key string) (*vaultpb.Manifest, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.GetManifest(ctx, bucket, key)
}

func (p *faultyPeer) ListManifests(ctx context.Context, req *vaultpb.ListManifestsRequest) ([]*vaultpb.Manifest, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.ListManifests(ctx, req)
}

func (p *faultyPeer) MerkleTree(ctx context.Context, peer string) ([][]byte, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.MerkleTree(ctx, peer)
}

func (p *faultyPeer) LeafEntries(ctx context.Context, peer string, leaf uint32) ([]*vaultpb.KeyVersion, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.LeafEntries(ctx, peer, leaf)
}

func (p *faultyPeer) SyncCatalog(ctx context.Context, c *vaultpb.Catalog) (*vaultpb.Catalog, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.inner.SyncCatalog(ctx, c)
}
