package node

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/events"
	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/store"
	"github.com/hydra-software/vault/engine/internal/transport"
)

// MerkleLeaves is the number of leaves in the anti-entropy tree.
const MerkleLeaves = 256

// Server returns the node's implementation of the node-to-node API.
func (n *Node) Server() transport.Peer { return &server{n: n} }

// selfPeer lets the coordinator treat the local node like any other peer.
func (n *Node) selfPeer() transport.Peer { return &server{n: n, local: true} }

type server struct {
	n     *Node
	local bool
}

func (s *server) guard(ctx context.Context) error {
	if s.local {
		return nil
	}
	caller := transport.Caller(ctx)
	if caller == "" {
		return nil
	}
	return s.n.faults.Check(ctx, caller, s.n.cfg.ID)
}

func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%w: %v", transport.ErrNotFound, err)
	case errors.Is(err, store.ErrCorrupt):
		return fmt.Errorf("%w: %v", transport.ErrCorrupt, err)
	default:
		return err
	}
}

func (s *server) Ping(ctx context.Context) (string, error) {
	if err := s.guard(ctx); err != nil {
		return "", err
	}
	return s.n.cfg.ID, nil
}

func (s *server) PutShard(ctx context.Context, hash string, data []byte) error {
	if err := s.guard(ctx); err != nil {
		return err
	}
	err := s.n.shards.Put(hash, data)
	if err == nil {
		s.n.Metrics.Counter("shard_bytes_written").Add(uint64(len(data)))
	}
	return mapStoreErr(err)
}

func (s *server) GetShard(ctx context.Context, hash string) ([]byte, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	data, err := s.n.shards.Get(hash)
	if errors.Is(err, store.ErrCorrupt) {
		s.n.onCorruptShard(hash, "read")
	}
	return data, mapStoreErr(err)
}

func (s *server) HasShards(ctx context.Context, hashes []string, verify bool) ([]bool, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	out := make([]bool, len(hashes))
	for i, h := range hashes {
		ok, err := s.n.shards.Has(h, false)
		if err != nil {
			return nil, err
		}
		if ok && verify {
			if err := s.n.shards.Verify(h); err != nil {
				if errors.Is(err, store.ErrCorrupt) {
					s.n.onCorruptShard(h, "verify")
				}
				ok = false
			}
		}
		out[i] = ok
	}
	return out, nil
}

func (s *server) PutManifest(ctx context.Context, m *vaultpb.Manifest, hintFor string) (bool, error) {
	if err := s.guard(ctx); err != nil {
		return false, err
	}
	if m == nil || keys.ValidateBucket(m.Bucket) != nil || keys.ValidateKey(m.Key) != nil || m.Version == nil || m.Policy == nil {
		return false, errors.New("invalid manifest")
	}
	s.n.clock.Observe(m.Version)
	return s.n.meta.PutManifest(m, hintFor)
}

func (s *server) GetManifest(ctx context.Context, bucket, key string) (*vaultpb.Manifest, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	return s.n.meta.GetManifest(bucket, key)
}

func (s *server) ListManifests(ctx context.Context, r *vaultpb.ListManifestsRequest) ([]*vaultpb.Manifest, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	limit := int(r.Limit)
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	return s.n.meta.ListManifests(r.Bucket, r.Prefix, r.StartAfter, limit)
}

func (s *server) MerkleTree(ctx context.Context, peer string) ([][]byte, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	return s.n.merkleLeaves(peer)
}

func (s *server) LeafEntries(ctx context.Context, peer string, leaf uint32) ([]*vaultpb.KeyVersion, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	return s.n.leafEntries(peer, leaf)
}

func (s *server) SyncCatalog(ctx context.Context, c *vaultpb.Catalog) (*vaultpb.Catalog, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	if _, err := s.n.catalog.Merge(c); err != nil {
		return nil, err
	}
	return s.n.catalog.Snapshot(), nil
}

// sharedKeys iterates local manifests owned by both this node and peer.
func (n *Node) sharedKeys(peer string, fn func(leaf uint32, m *vaultpb.Manifest)) error {
	v := n.currentView()
	return n.meta.ForEachManifest(func(m *vaultpb.Manifest) error {
		owners := v.preference(m.Bucket, m.Key)
		if w := int(m.GetPolicy().GetN()); w < len(owners) {
			owners = owners[:w]
		}
		self, other := false, false
		for _, o := range owners {
			self = self || o.ID == n.cfg.ID
			other = other || o.ID == peer
		}
		if self && other {
			h := keys.ObjectHash(m.Bucket, m.Key)
			fn(uint32(h[0]), m)
		}
		return nil
	})
}

func (n *Node) merkleLeaves(peer string) ([][]byte, error) {
	hashers := make([][]byte, MerkleLeaves)
	acc := make(map[uint32][]byte)
	err := n.sharedKeys(peer, func(leaf uint32, m *vaultpb.Manifest) {
		v := m.Version
		entry := fmt.Sprintf("%s\x00%s\x00%d.%d.%s\x00%t\n", m.Bucket, m.Key, v.GetWall(), v.GetLogical(), v.GetNode(), m.Deleted)
		acc[leaf] = append(acc[leaf], entry...)
	})
	if err != nil {
		return nil, err
	}
	for i := range hashers {
		sum := sha256.Sum256(acc[uint32(i)])
		hashers[i] = sum[:]
	}
	return hashers, nil
}

func (n *Node) leafEntries(peer string, leaf uint32) ([]*vaultpb.KeyVersion, error) {
	var out []*vaultpb.KeyVersion
	err := n.sharedKeys(peer, func(l uint32, m *vaultpb.Manifest) {
		if l == leaf {
			out = append(out, &vaultpb.KeyVersion{Bucket: m.Bucket, Key: m.Key, Version: m.Version})
		}
	})
	return out, err
}

// onCorruptShard records corruption and schedules repair of every object
// that references the shard.
func (n *Node) onCorruptShard(hash, how string) {
	n.Metrics.Counter("corruption_detected").Add(1)
	refs, _ := n.meta.ShardRefs(hash)
	for _, r := range refs {
		n.Events.Publish("corruption.detected", events.Warning, r.Bucket, r.Key,
			fmt.Sprintf("shard %s… failed its SHA-256 check during %s; scheduling repair", hash[:12], how))
		n.queue.enqueue(r, priorityCritical, "corruption")
	}
}
