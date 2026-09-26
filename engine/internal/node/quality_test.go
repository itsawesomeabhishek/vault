package node

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/policy"
)

// Identical payloads must share on-disk shards (content-addressed dedup),
// which is how Vault keeps storage overhead down for repeated studies.
func TestDedupIdenticalPayloadsShareDisk(t *testing.T) {
	c := newTestCluster(t, 1, nil)
	c.createBucket("scans", policy.Replicated(1, 1, 1, false))
	data := bytes.Repeat([]byte("CT"), 80_000)
	mustPut(t, c.node("n1"), "scans", "patients/1/a.dcm", data)
	before := c.node("n1").shards.UsedBytes()
	mustPut(t, c.node("n1"), "scans", "patients/2/b.dcm", data)
	after := c.node("n1").shards.UsedBytes()
	if after > before {
		t.Fatalf("second identical object grew local shard store %d → %d", before, after)
	}
}

// Keys may contain ".." because they are never used as file paths. Shards
// live under SHA-256 names, so a hostile key cannot escape the data directory.
func TestHostileKeyCannotEscapeDataDir(t *testing.T) {
	c := newTestCluster(t, 2, nil)
	c.createBucket("scans", policy.Replicated(2, 2, 1, false))
	key := "../../windows/system32/evil.dcm"
	data := []byte("not-a-path")
	mustPut(t, c.node("n1"), "scans", key, data)
	if got := mustGet(t, c.node("n1"), "scans", key); !bytes.Equal(got, data) {
		t.Fatal("round-trip of key containing .. failed")
	}
	root := c.node("n1").cfg.DataDir
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, "..") {
			t.Fatalf("wrote outside data dir: %s", p)
		}
		if !d.IsDir() && strings.Contains(filepath.Base(p), "evil") {
			t.Fatalf("hostile key leaked into a filename: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Streaming writes never hold the whole object: the pipeline has a 1-chunk
// buffer. A multi-chunk object must succeed and stay readable.
func TestStreamingPutMultiChunk(t *testing.T) {
	c := newTestCluster(t, 3, func(cfg *Config) { cfg.ChunkSize = 64 << 10 })
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	data := randomBytes(t, 300_000)
	m := mustPut(t, c.node("n1"), "scans", "big.dcm", data)
	if len(m.Chunks) < 4 {
		t.Fatalf("expected several 64KiB chunks, got %d", len(m.Chunks))
	}
	if got := mustGet(t, c.node("n2"), "scans", "big.dcm"); !bytes.Equal(got, data) {
		t.Fatal("streamed object did not round-trip")
	}
}

func TestMetricsRecordPutAndGet(t *testing.T) {
	c := newTestCluster(t, 2, nil)
	c.createBucket("scans", policy.Replicated(2, 2, 1, false))
	mustPut(t, c.node("n1"), "scans", "m.dcm", []byte("x"))
	_ = mustGet(t, c.node("n1"), "scans", "m.dcm")
	if c.node("n1").Metrics.Counter("put_ok").Load() == 0 {
		t.Fatal("put_ok not recorded")
	}
	if c.node("n1").Metrics.Histogram("put_latency").Summary().Count == 0 {
		t.Fatal("put latency not recorded")
	}
	if c.node("n1").Metrics.Counter("get_ok").Load() == 0 {
		t.Fatal("get_ok not recorded")
	}
}

func TestInvalidKeyRejected(t *testing.T) {
	c := newTestCluster(t, 1, nil)
	c.createBucket("scans", policy.Replicated(1, 1, 1, false))
	_, err := c.node("n1").Put(context.Background(), "scans", "bad\x00key", bytes.NewReader([]byte("x")), "application/octet-stream", nil)
	if err == nil || !strings.Contains(err.Error(), "control") {
		t.Fatalf("control character in key should fail, got %v", err)
	}
	if err := keys.ValidateKey(strings.Repeat("a", keys.MaxKeyLen+1)); err == nil {
		t.Fatal("overlong key accepted")
	}
}
