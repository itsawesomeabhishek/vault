// Package policy validates and describes bucket durability policies.
package policy

import (
	"errors"
	"fmt"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
)

// MaxWidth bounds the number of slots (replicas or shards) per object.
const MaxWidth = 16

// ErrInvalid is wrapped by every validation failure.
var ErrInvalid = errors.New("invalid policy")

// Replicated returns a full-replication policy.
func Replicated(n, w, r uint32, sloppy bool) *vaultpb.Policy {
	return &vaultpb.Policy{N: n, K: 1, W: w, R: r, Sloppy: sloppy}
}

// Erasure returns a Reed-Solomon policy with k data shards and m parity shards.
func Erasure(k, m, w, r uint32, sloppy bool) *vaultpb.Policy {
	return &vaultpb.Policy{N: k + m, K: k, W: w, R: r, Sloppy: sloppy}
}

// Validate enforces invariants required for durability and read-your-writes
// consistency: W >= K so every acknowledged write is decodable, and R + W > N
// so every read quorum intersects every write quorum.
func Validate(p *vaultpb.Policy) error {
	if p == nil {
		return fmt.Errorf("%w: missing", ErrInvalid)
	}
	switch {
	case p.N < 1 || p.N > MaxWidth:
		return fmt.Errorf("%w: n must be between 1 and %d", ErrInvalid, MaxWidth)
	case p.K < 1 || p.K > p.N:
		return fmt.Errorf("%w: k must be between 1 and n", ErrInvalid)
	case p.K > 1 && p.N == p.K:
		return fmt.Errorf("%w: erasure coding needs at least one parity shard", ErrInvalid)
	case p.W < p.K || p.W > p.N:
		return fmt.Errorf("%w: w must be between k and n", ErrInvalid)
	case p.R < 1 || p.R > p.N:
		return fmt.Errorf("%w: r must be between 1 and n", ErrInvalid)
	case p.R+p.W <= p.N:
		return fmt.Errorf("%w: r + w must be greater than n", ErrInvalid)
	}
	return nil
}

// IsErasure reports whether the policy uses erasure coding.
func IsErasure(p *vaultpb.Policy) bool { return p.K > 1 }

// Overhead is the raw bytes stored per logical byte (for example 3.0 or 1.5).
func Overhead(p *vaultpb.Policy) float64 { return float64(p.N) / float64(p.K) }

// FaultTolerance is how many slots can be lost while the object stays readable.
func FaultTolerance(p *vaultpb.Policy) uint32 { return p.N - p.K }

// AckFaultTolerance is how many slots can be lost immediately after a write is
// acknowledged, before background repair restores full width.
func AckFaultTolerance(p *vaultpb.Policy) uint32 { return p.W - p.K }

// ShardHash returns the content hash stored in the given slot for a chunk.
func ShardHash(p *vaultpb.Policy, c *vaultpb.Chunk, slot int) string {
	if p.K <= 1 || len(c.Shards) == 0 {
		return c.Hash
	}
	return c.Shards[slot]
}

// Describe returns a short human-readable summary.
func Describe(p *vaultpb.Policy) string {
	if IsErasure(p) {
		return fmt.Sprintf("erasure %d+%d (w=%d r=%d)", p.K, p.N-p.K, p.W, p.R)
	}
	return fmt.Sprintf("replicated x%d (w=%d r=%d)", p.N, p.W, p.R)
}
