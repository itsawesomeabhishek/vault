package policy

import (
	"errors"
	"testing"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		p    *vaultpb.Policy
		ok   bool
	}{
		{"default replicated", Replicated(3, 2, 2, true), true},
		{"single copy", Replicated(1, 1, 1, false), true},
		{"erasure 2+1", Erasure(2, 1, 2, 2, true), true},
		{"erasure 4+2", Erasure(4, 2, 5, 2, false), true},
		{"nil", nil, false},
		{"zero n", &vaultpb.Policy{N: 0, K: 1, W: 1, R: 1}, false},
		{"too wide", Replicated(MaxWidth+1, MaxWidth, 2, false), false},
		{"no quorum overlap", Replicated(3, 1, 1, false), false},
		{"w below k", Erasure(3, 1, 2, 3, false), false},
		{"erasure without parity", &vaultpb.Policy{N: 2, K: 2, W: 2, R: 1}, false},
		{"r too big", Replicated(3, 2, 4, false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.p)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

func TestOverheadAndTolerance(t *testing.T) {
	r := Replicated(3, 2, 2, true)
	if Overhead(r) != 3 || FaultTolerance(r) != 2 || AckFaultTolerance(r) != 1 {
		t.Fatal("unexpected replicated characteristics")
	}
	e := Erasure(2, 1, 3, 1, true)
	if Overhead(e) != 1.5 || FaultTolerance(e) != 1 || AckFaultTolerance(e) != 1 {
		t.Fatal("unexpected erasure characteristics")
	}
}

func TestShardHash(t *testing.T) {
	c := &vaultpb.Chunk{Hash: "chunk", Shards: []string{"s0", "s1", "s2"}}
	if got := ShardHash(Replicated(3, 2, 2, false), &vaultpb.Chunk{Hash: "chunk"}, 2); got != "chunk" {
		t.Fatalf("replicated slot hash = %q", got)
	}
	if got := ShardHash(Erasure(2, 1, 2, 2, false), c, 1); got != "s1" {
		t.Fatalf("erasure slot hash = %q", got)
	}
}
