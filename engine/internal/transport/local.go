package transport

import (
	"context"
	"fmt"
	"sync"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/membership"
	"google.golang.org/protobuf/proto"
)

// LocalNetwork connects in-process nodes without sockets. Messages are
// deep-copied so nodes never share mutable state, as over a real network.
type LocalNetwork struct {
	mu    sync.RWMutex
	peers map[string]Peer
}

// NewLocalNetwork returns an empty in-process network.
func NewLocalNetwork() *LocalNetwork { return &LocalNetwork{peers: map[string]Peer{}} }

// Register attaches a node's server-side Peer implementation.
func (n *LocalNetwork) Register(id string, p Peer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.peers[id] = p
}

// Unregister detaches a node (process exit).
func (n *LocalNetwork) Unregister(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.peers, id)
}

// Dialer returns a Dialer that identifies calls as coming from self.
func (n *LocalNetwork) Dialer(self string) Dialer { return localDialer{n: n, self: self} }

type localDialer struct {
	n    *LocalNetwork
	self string
}

func (d localDialer) Dial(m membership.Member) (Peer, error) {
	return &localPeer{n: d.n, self: d.self, target: m.ID}, nil
}

type localPeer struct {
	n            *LocalNetwork
	self, target string
}

func (p *localPeer) resolve(ctx context.Context) (context.Context, Peer, error) {
	p.n.mu.RLock()
	peer, ok := p.n.peers[p.target]
	p.n.mu.RUnlock()
	if !ok {
		return ctx, nil, fmt.Errorf("%w: %s is not running", ErrUnavailable, p.target)
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return WithCaller(ctx, p.self), peer, nil
}

func cloneManifest(m *vaultpb.Manifest) *vaultpb.Manifest {
	if m == nil {
		return nil
	}
	return proto.Clone(m).(*vaultpb.Manifest)
}

func (p *localPeer) Ping(ctx context.Context) (string, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return "", err
	}
	return peer.Ping(ctx)
}

func (p *localPeer) PutShard(ctx context.Context, hash string, data []byte) error {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return err
	}
	return peer.PutShard(ctx, hash, append([]byte(nil), data...))
}

func (p *localPeer) GetShard(ctx context.Context, hash string) ([]byte, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return peer.GetShard(ctx, hash)
}

func (p *localPeer) HasShards(ctx context.Context, hashes []string, verify bool) ([]bool, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return peer.HasShards(ctx, hashes, verify)
}

func (p *localPeer) PutManifest(ctx context.Context, m *vaultpb.Manifest, hintFor string) (bool, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return false, err
	}
	return peer.PutManifest(ctx, cloneManifest(m), hintFor)
}

func (p *localPeer) GetManifest(ctx context.Context, bucket, key string) (*vaultpb.Manifest, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	m, err := peer.GetManifest(ctx, bucket, key)
	return cloneManifest(m), err
}

func (p *localPeer) ListManifests(ctx context.Context, req *vaultpb.ListManifestsRequest) ([]*vaultpb.Manifest, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	list, err := peer.ListManifests(ctx, req)
	out := make([]*vaultpb.Manifest, len(list))
	for i, m := range list {
		out[i] = cloneManifest(m)
	}
	return out, err
}

func (p *localPeer) MerkleTree(ctx context.Context, peerID string) ([][]byte, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return peer.MerkleTree(ctx, peerID)
}

func (p *localPeer) LeafEntries(ctx context.Context, peerID string, leaf uint32) ([]*vaultpb.KeyVersion, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return peer.LeafEntries(ctx, peerID, leaf)
}

func (p *localPeer) SyncCatalog(ctx context.Context, c *vaultpb.Catalog) (*vaultpb.Catalog, error) {
	ctx, peer, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	out, err := peer.SyncCatalog(ctx, proto.Clone(c).(*vaultpb.Catalog))
	if out != nil {
		out = proto.Clone(out).(*vaultpb.Catalog)
	}
	return out, err
}
