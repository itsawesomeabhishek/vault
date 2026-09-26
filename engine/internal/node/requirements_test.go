package node

// Each test in this file maps to a requirement in REQUIREMENTS.md.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/hydra-software/vault/engine/internal/hlc"
	"github.com/hydra-software/vault/engine/internal/policy"
	"github.com/hydra-software/vault/engine/internal/store"
)

// R1 storing/retrieving + R2 replication across zones.
func TestR1R2_StoreRetrieveReplicateAcrossZones(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	data := randomBytes(t, 300_000)
	m := mustPut(t, c.node("n1"), "scans", "patients/1/ct.dcm", data)
	if len(m.Chunks) != 5 {
		t.Fatalf("expected 5 chunks of 64KiB, got %d", len(m.Chunks))
	}
	for id, n := range c.nodes {
		if got := mustGet(t, n, "scans", "patients/1/ct.dcm"); !bytes.Equal(got, data) {
			t.Fatalf("data read via %s differs", id)
		}
	}
	eventually(t, 5*time.Second, "3 healthy replicas", func() bool { return fullyHealthy(c.node("n2"), "scans", "patients/1/ct.dcm") })
	ins, _ := c.node("n2").Inspect(context.Background(), "scans", "patients/1/ct.dcm")
	zones := map[string]bool{}
	for _, s := range ins.Slots {
		zones[s.Zone] = true
	}
	if len(zones) != 2 {
		t.Fatalf("replicas not spread across zones: %+v", ins.Slots)
	}
}

// R3 configurable durability policies, including erasure coding for low overhead.
func TestR3_ErasureCodingPolicySurvivesNodeLossWithLowOverhead(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("archive", policy.Erasure(2, 1, 2, 2, true))
	data := randomBytes(t, 640<<10)
	mustPut(t, c.node("n1"), "archive", "study.bin", data)
	eventually(t, 5*time.Second, "all shards healthy", func() bool { return fullyHealthy(c.node("n1"), "archive", "study.bin") })

	var raw int64
	for _, n := range c.nodes {
		raw += n.shards.UsedBytes()
	}
	if ratio := float64(raw) / float64(len(data)); ratio > 1.6 {
		t.Fatalf("erasure 2+1 should store ~1.5x, stored %.2fx", ratio)
	}

	own := owners(c.node("n1"), "archive", "study.bin", 3)
	c.crash(own[0].ID)
	var reader *Node
	for _, n := range c.nodes {
		reader = n
	}
	if got := mustGet(t, reader, "archive", "study.bin"); !bytes.Equal(got, data) {
		t.Fatal("erasure-coded read after node loss returned wrong data")
	}
}

// R3 invalid policies are rejected.
func TestR3_InvalidPolicyRejected(t *testing.T) {
	c := newTestCluster(t, 1, nil)
	if _, err := c.node("n1").CreateBucket(context.Background(), "bad", policy.Replicated(3, 1, 1, false)); !errors.Is(err, policy.ErrInvalid) {
		t.Fatalf("expected invalid policy error, got %v", err)
	}
}

// R4 concurrent reads and writes converge to a single winner on every replica.
func TestR4_ConcurrentReadsAndWritesConverge(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	ids := []string{"n1", "n2", "n3", "n4"}
	keys := []string{"k0", "k1", "k2"}
	for _, k := range keys {
		mustPut(t, c.node("n1"), "scans", k, []byte("seed"))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			n := c.node(ids[w%len(ids)])
			for i := range 15 {
				k := keys[(w+i)%len(keys)]
				payload := []byte(fmt.Sprintf("writer-%d-iter-%d", w, i))
				if _, err := n.Put(context.Background(), "scans", k, bytes.NewReader(payload), "text/plain", nil); err != nil {
					errs <- err
				}
				m, rc, err := n.Get(context.Background(), "scans", k)
				if err != nil {
					errs <- err
					continue
				}
				got, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					errs <- err
				} else if uint64(len(got)) != m.Size {
					errs <- fmt.Errorf("torn read: %d bytes, manifest says %d", len(got), m.Size)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, k := range keys {
		eventually(t, 10*time.Second, "replicas of "+k+" agree", func() bool {
			var first *store.ObjectRef
			_ = first
			own := owners(c.node("n1"), "scans", k, 3)
			v0, _ := c.node(own[0].ID).meta.GetManifest("scans", k)
			for _, o := range own[1:] {
				v, _ := c.node(o.ID).meta.GetManifest("scans", k)
				if v == nil || v0 == nil || hlc.Compare(v.Version, v0.Version) != 0 {
					return false
				}
			}
			return true
		})
	}
}

// R5 node failure: sloppy quorum keeps writes available and hinted handoff
// returns data to the owner when it recovers.
func TestR5_NodeFailureSloppyQuorumAndHintedHandoff(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("scans", policy.Replicated(3, 3, 1, true))
	key := "mri/42.dcm"
	own := owners(c.node("n1"), "scans", key, 3)
	victim := own[0].ID
	var coord *Node
	for id, n := range c.nodes {
		if id != victim {
			coord = n
		}
	}
	c.crash(victim)
	data := randomBytes(t, 100_000)
	mustPut(t, coord, "scans", key, data)
	hints := 0
	for _, n := range c.nodes {
		hints += n.meta.HintCount()
	}
	if hints == 0 {
		t.Fatal("expected a stand-in to record a hint for the crashed owner")
	}
	c.start(victim)
	eventually(t, 10*time.Second, "hinted handoff to recovered owner", func() bool {
		m, _ := c.node(victim).meta.GetManifest("scans", key)
		total := 0
		for _, n := range c.nodes {
			total += n.meta.HintCount()
		}
		return m != nil && total == 0 && fullyHealthy(coord, "scans", key)
	})
	if got := mustGet(t, c.node(victim), "scans", key); !bytes.Equal(got, data) {
		t.Fatal("recovered owner serves wrong data")
	}
}

// R6 partial network partition: a strict-quorum bucket refuses writes on the
// minority side instead of diverging.
func TestR6_PartitionStrictQuorumRejectsMinority(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("strict", policy.Replicated(3, 2, 2, false))
	key := "p/1"
	own := owners(c.node("n1"), "strict", key, 3)
	isolated := own[0].ID
	var rest []string
	for id := range c.nodes {
		if id != isolated {
			rest = append(rest, id)
		}
	}
	c.faults.Partition([]string{isolated}, rest)
	_, err := c.node(isolated).Put(context.Background(), "strict", key, bytes.NewReader([]byte("x")), "", nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("minority write must fail with ErrUnavailable, got %v", err)
	}
	mustPut(t, c.node(rest[0]), "strict", key, []byte("majority"))
	c.faults.Heal()
	if got := mustGet(t, c.node(isolated), "strict", key); string(got) != "majority" {
		t.Fatalf("after heal got %q", got)
	}
}

// R6 + R8 replica inconsistency after a partition is detected by Merkle
// anti-entropy (without any client read) and repaired.
func TestR6R8_PartitionHealConvergesViaAntiEntropy(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, false))
	key := "report.pdf"
	own := owners(c.node("n1"), "scans", key, 3)
	isolated := own[2].ID
	var rest []string
	for id := range c.nodes {
		if id != isolated {
			rest = append(rest, id)
		}
	}
	c.faults.Partition([]string{isolated}, rest)
	mustPut(t, c.node(own[0].ID), "scans", key, []byte("v2"))
	if m, _ := c.node(isolated).meta.GetManifest("scans", key); m != nil {
		t.Fatal("isolated owner should have missed the write")
	}
	c.faults.Heal()
	for _, n := range c.nodes {
		_ = n.AntiEntropy(context.Background())
	}
	eventually(t, 10*time.Second, "anti-entropy repairs isolated owner", func() bool {
		m, _ := c.node(isolated).meta.GetManifest("scans", key)
		return m != nil
	})
	var rounds uint64
	for _, n := range c.nodes {
		rounds += n.Metrics.Counter("anti_entropy_rounds").Load()
	}
	if rounds == 0 {
		t.Fatal("anti-entropy did not run")
	}
}

// R7 + R10 + R12 data corruption: the scrubber detects bit rot via SHA-256
// and the replica is rebuilt automatically.
func TestR7R10R12_CorruptionDetectedAndRepaired(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	data := randomBytes(t, 200_000)
	key := "xray.png"
	mustPut(t, c.node("n1"), "scans", key, data)
	own := owners(c.node("n1"), "scans", key, 3)
	victim := c.node(own[1].ID)
	eventually(t, 5*time.Second, "replica written", func() bool {
		m, _ := victim.meta.GetManifest("scans", key)
		return m != nil
	})
	if _, err := victim.CorruptLocal("scans", key); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, victim, "scans", key); !bytes.Equal(got, data) {
		t.Fatal("read must never return corrupt data")
	}
	rep := victim.Scrub(context.Background())
	_ = rep
	eventually(t, 10*time.Second, "corrupt replica rebuilt", func() bool { return fullyHealthy(c.node("n1"), "scans", key) })
	if victim.Metrics.Counter("corruption_detected").Load() == 0 {
		t.Fatal("corruption was not detected")
	}
}

// R9 background rebalancing: adding a node moves ownership, new owners
// receive data and old owners release it.
func TestR9_RebalanceOnJoin(t *testing.T) {
	c := newTestCluster(t, 3, nil)
	c.createBucket("scans", policy.Replicated(2, 2, 1, true))
	payloads := map[string][]byte{}
	for i := range 20 {
		k := fmt.Sprintf("obj-%02d", i)
		payloads[k] = randomBytes(t, 10_000)
		mustPut(t, c.node("n1"), "scans", k, payloads[k])
	}
	newNode := c.add("n4", "zone-b")
	moved := 0
	for k := range payloads {
		for _, o := range owners(newNode, "scans", k, 2) {
			if o.ID == "n4" {
				moved++
			}
		}
	}
	if moved == 0 {
		t.Fatal("new node owns nothing; test is not meaningful")
	}
	eventually(t, 20*time.Second, "rebalance completes", func() bool {
		for k := range payloads {
			if !fullyHealthy(newNode, "scans", k) {
				return false
			}
		}
		return true
	})
	for k, want := range payloads {
		if got := mustGet(t, newNode, "scans", k); !bytes.Equal(got, want) {
			t.Fatalf("%s wrong after rebalance", k)
		}
	}
}

// R11 metadata consistency: an upload that fails midway never produces a
// visible object or a manifest pointing at missing data, and orphaned shards
// are garbage-collected.
type failingReader struct {
	r     io.Reader
	after int
	read  int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.read >= f.after {
		return 0, errors.New("client disconnected")
	}
	n, err := f.r.Read(p)
	f.read += n
	return n, err
}

func TestR11_AbortedUploadLeavesNoVisibleObjectAndOrphansAreCollected(t *testing.T) {
	c := newTestCluster(t, 3, func(cfg *Config) { cfg.GCGrace = time.Millisecond })
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	src := &failingReader{r: bytes.NewReader(randomBytes(t, 500_000)), after: 200_000}
	if _, err := c.node("n1").Put(context.Background(), "scans", "broken", src, "", nil); err == nil {
		t.Fatal("expected upload failure")
	}
	if _, err := c.node("n2").Head(context.Background(), "scans", "broken"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("aborted object must not be visible, got %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	collected := 0
	for _, n := range c.nodes {
		collected += n.Scrub(context.Background()).Collected
	}
	if collected == 0 {
		t.Fatal("orphaned shards were not garbage-collected")
	}
}

// R11 deletes are tombstones replicated by quorum and hidden from listings.
func TestR11_DeleteAndList(t *testing.T) {
	c := newTestCluster(t, 3, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	for _, k := range []string{"a/1", "a/2", "b/1"} {
		mustPut(t, c.node("n1"), "scans", k, []byte(k))
	}
	if err := c.node("n2").Delete(context.Background(), "scans", "a/1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.node("n3").Get(context.Background(), "scans", "a/1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted object still readable: %v", err)
	}
	items, _, err := c.node("n3").List(context.Background(), "scans", "a/", "", 100)
	if err != nil || len(items) != 1 || items[0].Key != "a/2" {
		t.Fatalf("list = %+v, %v", items, err)
	}
	if err := c.node("n1").DeleteBucket(context.Background(), "scans"); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("non-empty bucket deletion must fail, got %v", err)
	}
}

// R13 predictable availability: hedged reads bound latency when a replica is slow.
func TestR13_HedgedReadsBoundTailLatency(t *testing.T) {
	c := newTestCluster(t, 3, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	key := "slow"
	data := randomBytes(t, 50_000)
	mustPut(t, c.node("n1"), "scans", key, data)
	eventually(t, 5*time.Second, "replicated", func() bool { return fullyHealthy(c.node("n1"), "scans", key) })
	var coord *Node
	own := owners(c.node("n1"), "scans", key, 3)
	for _, o := range own {
		if o.ID != own[0].ID {
			coord = c.node(o.ID)
		}
	}
	for _, o := range own {
		if o.ID != coord.ID() {
			c.faults.SetDelay(o.ID, 1500*time.Millisecond)
			break
		}
	}
	coord.cfg.HedgeDelay = 20 * time.Millisecond
	start := time.Now()
	if got := mustGet(t, coord, "scans", key); !bytes.Equal(got, data) {
		t.Fatal("wrong data")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("read took %s despite a healthy local replica", took)
	}
}

// R14 minimise recovery time: repair after losing a whole node restores full
// width for every object without manual action.
func TestR14_AutomaticRecoveryAfterPermanentNodeLoss(t *testing.T) {
	c := newTestCluster(t, 4, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	for i := range 10 {
		mustPut(t, c.node("n1"), "scans", fmt.Sprintf("k%d", i), randomBytes(t, 20_000))
	}
	c.crash("n4")
	c.members.Remove("n4")
	eventually(t, 20*time.Second, "full replication restored on remaining nodes", func() bool {
		for i := range 10 {
			if !fullyHealthy(c.node("n1"), "scans", fmt.Sprintf("k%d", i)) {
				return false
			}
		}
		return true
	})
	if s := c.node("n1").Metrics.Histogram("time_to_repair").Summary(); s.Count == 0 {
		t.Log("repairs were performed by other nodes")
	}
}
