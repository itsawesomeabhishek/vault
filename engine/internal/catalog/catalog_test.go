package catalog

import (
	"errors"
	"testing"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/hlc"
)

func TestCatalogMergeLastWriterWins(t *testing.T) {
	old := &vaultpb.BucketConfig{Name: "scans", Version: &vaultpb.Version{Wall: 1, Node: "a"}}
	c := New([]*vaultpb.BucketConfig{old}, nil)
	if _, ok := c.Get("scans"); !ok {
		t.Fatal("seed missing")
	}
	newer := &vaultpb.BucketConfig{Name: "scans", Version: &vaultpb.Version{Wall: 2, Node: "b"}, Deleted: true}
	changed, err := c.Merge(&vaultpb.Catalog{Buckets: []*vaultpb.BucketConfig{newer}})
	if err != nil || !changed {
		t.Fatalf("merge newer: %v changed=%v", err, changed)
	}
	if _, ok := c.Get("scans"); ok {
		t.Fatal("deleted bucket still live")
	}
	if n := len(c.List()); n != 0 {
		t.Fatalf("list live=%d", n)
	}
	if snap := c.Snapshot(); len(snap.Buckets) != 1 || !snap.Buckets[0].Deleted {
		t.Fatal("snapshot should keep the tombstone")
	}
	stale := &vaultpb.BucketConfig{Name: "scans", Version: &vaultpb.Version{Wall: 1, Node: "z"}}
	changed, err = c.Merge(&vaultpb.Catalog{Buckets: []*vaultpb.BucketConfig{stale}})
	if err != nil || changed {
		t.Fatalf("stale write must not win: %v %v", err, changed)
	}
	if !hlc.Newer(newer.Version, stale.Version) {
		t.Fatal("fixture versions")
	}
}

func TestCatalogPersistError(t *testing.T) {
	boom := errors.New("disk full")
	c := New(nil, func([]*vaultpb.BucketConfig) error { return boom })
	_, err := c.Merge(&vaultpb.Catalog{Buckets: []*vaultpb.BucketConfig{{Name: "a", Version: &vaultpb.Version{Wall: 1}}}})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
