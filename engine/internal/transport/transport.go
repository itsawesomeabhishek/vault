// Package transport defines the node-to-node API and its implementations:
// gRPC over mutual TLS for production and an in-process dialer for tests,
// both of which can be wrapped with fault injection.
package transport

import (
	"context"
	"errors"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/membership"
)

// Errors shared by all transports.
var (
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("peer unavailable")
	ErrCorrupt     = errors.New("data corrupt")
)

// Peer is the API each node exposes to other nodes.
type Peer interface {
	Ping(ctx context.Context) (string, error)
	PutShard(ctx context.Context, hash string, data []byte) error
	GetShard(ctx context.Context, hash string) ([]byte, error)
	HasShards(ctx context.Context, hashes []string, verify bool) ([]bool, error)
	PutManifest(ctx context.Context, m *vaultpb.Manifest, hintFor string) (bool, error)
	GetManifest(ctx context.Context, bucket, key string) (*vaultpb.Manifest, error)
	ListManifests(ctx context.Context, req *vaultpb.ListManifestsRequest) ([]*vaultpb.Manifest, error)
	MerkleTree(ctx context.Context, peer string) ([][]byte, error)
	LeafEntries(ctx context.Context, peer string, leaf uint32) ([]*vaultpb.KeyVersion, error)
	SyncCatalog(ctx context.Context, c *vaultpb.Catalog) (*vaultpb.Catalog, error)
}

// Dialer returns a Peer for a member.
type Dialer interface {
	Dial(m membership.Member) (Peer, error)
}

type callerKey struct{}

// WithCaller records the authenticated calling node in ctx.
func WithCaller(ctx context.Context, node string) context.Context {
	return context.WithValue(ctx, callerKey{}, node)
}

// Caller returns the authenticated calling node, if known.
func Caller(ctx context.Context) string {
	s, _ := ctx.Value(callerKey{}).(string)
	return s
}
