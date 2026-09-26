package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/membership"
)

type pingPeer string

func (s pingPeer) Ping(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(s), nil
}
func (pingPeer) PutShard(context.Context, string, []byte) error { return nil }
func (pingPeer) GetShard(context.Context, string) ([]byte, error) {
	return nil, ErrNotFound
}
func (pingPeer) HasShards(context.Context, []string, bool) ([]bool, error) {
	return nil, nil
}
func (pingPeer) PutManifest(context.Context, *vaultpb.Manifest, string) (bool, error) {
	return true, nil
}
func (pingPeer) GetManifest(context.Context, string, string) (*vaultpb.Manifest, error) {
	return nil, ErrNotFound
}
func (pingPeer) ListManifests(context.Context, *vaultpb.ListManifestsRequest) ([]*vaultpb.Manifest, error) {
	return nil, nil
}
func (pingPeer) MerkleTree(context.Context, string) ([][]byte, error) { return nil, nil }
func (pingPeer) LeafEntries(context.Context, string, uint32) ([]*vaultpb.KeyVersion, error) {
	return nil, nil
}
func (pingPeer) SyncCatalog(context.Context, *vaultpb.Catalog) (*vaultpb.Catalog, error) {
	return nil, nil
}

func TestLocalDialAndUnregister(t *testing.T) {
	netw := NewLocalNetwork()
	netw.Register("n2", pingPeer("n2"))
	p, err := netw.Dialer("n1").Dial(membership.Member{ID: "n2"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Ping(context.Background())
	if err != nil || id != "n2" {
		t.Fatalf("ping %q %v", id, err)
	}
	if Caller(WithCaller(context.Background(), "n1")) != "n1" {
		t.Fatal("caller")
	}
	netw.Unregister("n2")
	if _, err := p.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func TestFaultsPartitionAndHeal(t *testing.T) {
	f := NewFaults()
	ctx := context.Background()
	f.Partition([]string{"a"}, []string{"b"})
	if err := f.Check(ctx, "a", "b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("partition: %v", err)
	}
	f.SetDown("c", true)
	if err := f.Check(ctx, "a", "c"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("down: %v", err)
	}
	f.SetDelay("b", 5*time.Millisecond)
	start := time.Now()
	if err := f.Check(ctx, "x", "b"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 5*time.Millisecond {
		t.Fatal("delay not applied")
	}
	f.Heal()
	if err := f.Check(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
}
