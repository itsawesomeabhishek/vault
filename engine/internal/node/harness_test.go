package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/transport"
)

// testCluster runs several nodes in one process over an in-memory network
// with fault injection. Membership is controlled explicitly by the test.
type testCluster struct {
	t       testing.TB
	members *membership.Static
	net     *transport.LocalNetwork
	faults  *transport.Faults
	nodes   map[string]*Node
	dirs    map[string]string
	zones   map[string]string
	tweak   func(*Config)
}

func newTestCluster(t testing.TB, count int, tweak func(*Config)) *testCluster {
	t.Helper()
	c := &testCluster{
		t: t, members: membership.NewStatic(), net: transport.NewLocalNetwork(), faults: transport.NewFaults(),
		nodes: map[string]*Node{}, dirs: map[string]string{}, zones: map[string]string{}, tweak: tweak,
	}
	for i := range count {
		c.add(fmt.Sprintf("n%d", i+1), fmt.Sprintf("zone-%c", 'a'+i%2))
	}
	t.Cleanup(c.closeAll)
	return c
}

func (c *testCluster) config(id string) Config {
	cfg := Config{
		ID: id, Zone: c.zones[id], DataDir: c.dirs[id], ChunkSize: 64 << 10,
		RPCTimeout: 2 * time.Second, HedgeDelay: 30 * time.Millisecond, RepairWorkers: 4,
		ScrubInterval: time.Hour, AntiEntropyInterval: 200 * time.Millisecond, RebalanceInterval: time.Second,
		GCGrace: time.Hour, TombstoneTTL: time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if c.tweak != nil {
		c.tweak(&cfg)
	}
	return cfg
}

func (c *testCluster) add(id, zone string) *Node {
	c.t.Helper()
	c.dirs[id] = c.t.TempDir()
	c.zones[id] = zone
	return c.start(id)
}

func (c *testCluster) start(id string) *Node {
	c.t.Helper()
	n, err := New(c.config(id), c.members.View(id), c.net.Dialer(id), c.faults)
	if err != nil {
		c.t.Fatal(err)
	}
	c.nodes[id] = n
	c.net.Register(id, n.Server())
	c.members.Upsert(membership.Member{ID: id, Zone: c.zones[id], State: membership.Alive})
	n.Start()
	return n
}

// crash stops a node abruptly; membership marks it dead but it stays in the ring.
func (c *testCluster) crash(id string) {
	c.t.Helper()
	c.net.Unregister(id)
	c.members.SetState(id, membership.Dead)
	if n := c.nodes[id]; n != nil {
		_ = n.Close()
		delete(c.nodes, id)
	}
}

func (c *testCluster) closeAll() {
	for id := range c.nodes {
		c.net.Unregister(id)
		_ = c.nodes[id].Close()
	}
}

func (c *testCluster) node(id string) *Node { return c.nodes[id] }

func (c *testCluster) createBucket(name string, p *vaultpb.Policy) {
	c.t.Helper()
	for _, n := range c.nodes {
		if _, err := n.CreateBucket(context.Background(), name, p); err != nil {
			c.t.Fatal(err)
		}
		return
	}
}

func randomBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func mustPut(t testing.TB, n *Node, bucket, key string, data []byte) *vaultpb.Manifest {
	t.Helper()
	m, err := n.Put(context.Background(), bucket, key, bytes.NewReader(data), "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("put %s/%s via %s: %v", bucket, key, n.ID(), err)
	}
	return m
}

func mustGet(t testing.TB, n *Node, bucket, key string) []byte {
	t.Helper()
	_, rc, err := n.Get(context.Background(), bucket, key)
	if err != nil {
		t.Fatalf("get %s/%s via %s: %v", bucket, key, n.ID(), err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s/%s via %s: %v", bucket, key, n.ID(), err)
	}
	return data
}

func eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}

func fullyHealthy(n *Node, bucket, key string) bool {
	ins, err := n.Inspect(context.Background(), bucket, key)
	return err == nil && ins.Healthy == ins.Width && len(ins.ExtraHolders) == 0
}

func owners(n *Node, bucket, key string, width int) []membership.Member {
	pref := n.currentView().preference(bucket, key)
	return pref[:min(width, len(pref))]
}
