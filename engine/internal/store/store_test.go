package store

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/keys"
)

func TestShardPutGetDedupAndQuota(t *testing.T) {
	s, err := OpenShards(t.TempDir(), ShardOptions{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("12345")
	h := keys.HashHex(data)
	for range 3 {
		if err := s.Put(h, data); err != nil {
			t.Fatal(err)
		}
	}
	if s.UsedBytes() != 5 {
		t.Fatalf("dedup failed: used=%d", s.UsedBytes())
	}
	got, err := s.Get(h)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get: %v %q", err, got)
	}
	big := []byte("0123456789")
	if err := s.Put(keys.HashHex(big), big); !errors.Is(err, ErrQuota) {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestShardRejectsMismatchedHashAndTraversal(t *testing.T) {
	s, err := OpenShards(t.TempDir(), ShardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(keys.HashHex([]byte("a")), []byte("b")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
	if _, err := s.Get("../../../../etc/passwd"); !errors.Is(err, keys.ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestCorruptionDetectedAndQuarantined(t *testing.T) {
	for _, enc := range []bool{false, true} {
		var key []byte
		if enc {
			key = bytes.Repeat([]byte{7}, 32)
		}
		s, err := OpenShards(t.TempDir(), ShardOptions{EncryptionKey: key})
		if err != nil {
			t.Fatal(err)
		}
		data := []byte("medical image bytes")
		h := keys.HashHex(data)
		if err := s.Put(h, data); err != nil {
			t.Fatal(err)
		}
		if err := s.CorruptForTesting(h); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(h); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("encrypted=%v: expected ErrCorrupt, got %v", enc, err)
		}
		if ok, _ := s.Has(h, false); ok {
			t.Fatal("corrupt shard should be quarantined")
		}
		if s.UsedBytes() != 0 {
			t.Fatalf("used bytes not released: %d", s.UsedBytes())
		}
	}
}

func TestEncryptionAtRestHidesPlaintext(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenShards(dir, ShardOptions{EncryptionKey: bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("PATIENT NAME: JANE DOE")
	h := keys.HashHex(data)
	if err := s.Put(h, data); err != nil {
		t.Fatal(err)
	}
	found := false
	_ = s.Walk(func(hash string, _ int64, _ time.Time) error {
		found = hash == h
		return nil
	})
	if !found {
		t.Fatal("walk did not find shard")
	}
	raw, err := readRaw(s, h)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("JANE")) {
		t.Fatal("plaintext visible on disk")
	}
}

func readRaw(s *ShardStore, h string) ([]byte, error) {
	return osReadFile(s.path(h))
}

func manifest(key string, wall int64, shards ...string) *vaultpb.Manifest {
	m := &vaultpb.Manifest{Bucket: "scans", Key: key, Version: &vaultpb.Version{Wall: wall, Node: "n1"}}
	for _, s := range shards {
		m.Chunks = append(m.Chunks, &vaultpb.Chunk{Hash: s})
	}
	return m
}

func TestManifestLastWriterWinsAndRefs(t *testing.T) {
	m, err := OpenMeta(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	h1, h2 := keys.HashHex([]byte("1")), keys.HashHex([]byte("2"))

	if ok, err := m.PutManifest(manifest("a", 10, h1), ""); !ok || err != nil {
		t.Fatalf("first put: %v %v", ok, err)
	}
	if ok, _ := m.PutManifest(manifest("a", 5, h2), ""); ok {
		t.Fatal("older version must not overwrite")
	}
	if ok, _ := m.PutManifest(manifest("a", 20, h2), "node-x"); !ok {
		t.Fatal("newer version must overwrite")
	}
	if m.HasRefs(h1) || !m.HasRefs(h2) {
		t.Fatal("refs not updated on overwrite")
	}
	refs, _ := m.ShardRefs(h2)
	if len(refs) != 1 || refs[0].Key != "a" {
		t.Fatalf("refs = %+v", refs)
	}
	hints, _ := m.Hints("node-x")
	if len(hints) != 1 {
		t.Fatalf("hints = %+v", hints)
	}
	if err := m.DeleteHintsFor("scans", "a"); err != nil || m.HintCount() != 0 {
		t.Fatalf("hint cleanup failed: %v", err)
	}
	got, _ := m.GetManifest("scans", "a")
	if got.Version.Wall != 20 {
		t.Fatalf("got version %d", got.Version.Wall)
	}
	if dropped, _ := m.DropManifest("scans", "a", &vaultpb.Version{Wall: 10, Node: "n1"}); dropped {
		t.Fatal("drop with stale version must be refused")
	}
	if dropped, _ := m.DropManifest("scans", "a", got.Version); !dropped {
		t.Fatal("drop with current version must succeed")
	}
	if m.HasRefs(h2) {
		t.Fatal("refs remain after drop")
	}
}

func TestListAndTombstonePurge(t *testing.T) {
	m, err := OpenMeta(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for i, k := range []string{"p/1", "p/2", "p/3", "q/1"} {
		if _, err := m.PutManifest(manifest(k, int64(i+1)), ""); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := m.ListManifests("scans", "p/", "", 0)
	if len(list) != 3 {
		t.Fatalf("list = %d", len(list))
	}
	page, _ := m.ListManifests("scans", "p/", "p/1", 1)
	if len(page) != 1 || page[0].Key != "p/2" {
		t.Fatalf("page = %+v", page)
	}
	ts := manifest("p/1", time.Now().Add(-time.Hour).UnixNano())
	ts.Deleted = true
	if _, err := m.PutManifest(ts, ""); err != nil {
		t.Fatal(err)
	}
	n, err := m.PurgeTombstones(time.Now().Add(-time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	live, tomb, _ := m.Counts()
	if live != 3 || tomb != 0 {
		t.Fatalf("counts live=%d tomb=%d", live, tomb)
	}
}
