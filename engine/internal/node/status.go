package node

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/erasure"
	"github.com/hydra-software/vault/engine/internal/hlc"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/metrics"
	"github.com/hydra-software/vault/engine/internal/policy"
	"github.com/hydra-software/vault/engine/internal/store"
)

func erasureReconstruct(shards [][]byte, p *vaultpb.Policy) error {
	return erasure.Reconstruct(shards, int(p.K), int(p.N-p.K))
}

// SlotStatus describes one replica/shard slot of an object.
type SlotStatus struct {
	Slot          int    `json:"slot"`
	Node          string `json:"node"`
	Zone          string `json:"zone"`
	Alive         bool   `json:"alive"`
	HasManifest   bool   `json:"hasManifest"`
	VersionMatch  bool   `json:"versionMatch"`
	ShardsPresent int    `json:"shardsPresent"`
	ShardsTotal   int    `json:"shardsTotal"`
	Healthy       bool   `json:"healthy"`
	Error         string `json:"error,omitempty"`
}

// Inspection is the full placement and integrity report of an object.
type Inspection struct {
	Object         ObjectSummary `json:"object"`
	Width          int           `json:"width"`
	DataShards     int           `json:"dataShards"`
	Healthy        int           `json:"healthySlots"`
	Readable       bool          `json:"readable"`
	Overhead       float64       `json:"overhead"`
	FaultTolerance int           `json:"faultTolerance"`
	Slots          []SlotStatus  `json:"slots"`
	ExtraHolders   []string      `json:"extraHolders"`
}

// Inspect verifies every slot of an object (re-hashing the stored data) and
// schedules repair when anything is missing, stale or corrupt.
func (n *Node) Inspect(ctx context.Context, bucket, key string) (*Inspection, error) {
	m, err := n.Head(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	p := m.Policy
	pref := n.currentView().preference(bucket, key)
	ins := &Inspection{
		Object: Summarize(m), Width: int(p.N), DataShards: int(p.K),
		Overhead: policy.Overhead(p), FaultTolerance: int(policy.FaultTolerance(p)),
	}
	for slot := range int(p.N) {
		st := SlotStatus{Slot: slot, ShardsTotal: len(m.Chunks)}
		if slot >= len(pref) {
			st.Error = "no node assigned (cluster smaller than policy width)"
			ins.Slots = append(ins.Slots, st)
			continue
		}
		o := pref[slot]
		st.Node, st.Zone, st.Alive = o.ID, o.Zone, o.IsAlive()
		if !st.Alive {
			st.Error = "node unreachable"
			ins.Slots = append(ins.Slots, st)
			continue
		}
		n.inspectSlot(ctx, m, slot, o, &st)
		if st.Healthy {
			ins.Healthy++
		}
		ins.Slots = append(ins.Slots, st)
	}
	ins.Readable = ins.Healthy >= int(p.K)
	for i := int(p.N); i < len(pref); i++ {
		if !pref[i].IsAlive() {
			continue
		}
		if have, err := n.getManifestFrom(ctx, pref[i], store.ObjectRef{Bucket: bucket, Key: key}); err == nil && have != nil {
			ins.ExtraHolders = append(ins.ExtraHolders, pref[i].ID)
		}
	}
	if ins.Healthy < int(p.N) || len(ins.ExtraHolders) > 0 {
		n.queue.enqueue(store.ObjectRef{Bucket: bucket, Key: key}, priorityCritical, "inspection found unhealthy slots")
	}
	return ins, nil
}

func (n *Node) inspectSlot(ctx context.Context, m *vaultpb.Manifest, slot int, o membership.Member, st *SlotStatus) {
	peer, err := n.peer(o)
	if err != nil {
		st.Error = err.Error()
		return
	}
	cctx, cancel := n.rpcCtx(ctx)
	defer cancel()
	have, err := peer.GetManifest(cctx, m.Bucket, m.Key)
	if err != nil {
		st.Error = err.Error()
		return
	}
	st.HasManifest = have != nil
	st.VersionMatch = have != nil && hlc.Compare(have.Version, m.Version) == 0
	hashes := make([]string, len(m.Chunks))
	for i, c := range m.Chunks {
		hashes[i] = policy.ShardHash(m.Policy, c, slot)
	}
	present, err := peer.HasShards(cctx, hashes, true)
	if err != nil {
		st.Error = err.Error()
		return
	}
	for _, ok := range present {
		if ok {
			st.ShardsPresent++
		}
	}
	st.Healthy = st.VersionMatch && st.ShardsPresent == st.ShardsTotal
	if !st.Healthy && st.Error == "" {
		switch {
		case !st.HasManifest:
			st.Error = "manifest missing"
		case !st.VersionMatch:
			st.Error = "stale version"
		default:
			st.Error = fmt.Sprintf("%d shard(s) missing or corrupt", st.ShardsTotal-st.ShardsPresent)
		}
	}
}

// MemberStatus is a member as reported by the API.
type MemberStatus struct {
	membership.Member
	State string  `json:"state"`
	Share float64 `json:"share"`
	Self  bool    `json:"self"`
}

// Status is a node's health report.
type Status struct {
	ID            string                     `json:"id"`
	Zone          string                     `json:"zone"`
	UsedBytes     int64                      `json:"usedBytes"`
	MaxBytes      int64                      `json:"maxBytes"`
	Objects       int                        `json:"objects"`
	Tombstones    int                        `json:"tombstones"`
	LogicalBytes  uint64                     `json:"logicalBytes"`
	Hints         int                        `json:"hints"`
	RepairQueue   int                        `json:"repairQueue"`
	Members       []MemberStatus             `json:"members"`
	Partitioned   []string                   `json:"partitionedFrom"`
	Counters      map[string]uint64          `json:"counters"`
	Latencies     map[string]metrics.Summary `json:"latencies"`
	GeneratedAt   time.Time                  `json:"generatedAt"`
	EncryptAtRest bool                       `json:"encryptAtRest"`
}

// Status reports local health and the cluster view.
func (n *Node) Status() Status {
	v := n.currentView()
	share := v.ring.Share(1, 2048)
	live, tomb, logical := n.meta.Counts()
	counters, lat := n.Metrics.Snapshot()
	st := Status{
		ID: n.cfg.ID, Zone: n.cfg.Zone, UsedBytes: n.shards.UsedBytes(), MaxBytes: n.shards.MaxBytes(),
		Objects: live, Tombstones: tomb, LogicalBytes: logical, Hints: n.meta.HintCount(), RepairQueue: n.queue.depth(),
		Partitioned: n.faults.Blocked(n.cfg.ID), Counters: counters, Latencies: lat, GeneratedAt: time.Now().UTC(),
		EncryptAtRest: len(n.cfg.EncryptionKey) > 0,
	}
	for _, m := range v.members {
		st.Members = append(st.Members, MemberStatus{Member: m, State: m.State.String(), Share: share[m.ID], Self: m.ID == n.cfg.ID})
	}
	sort.Slice(st.Members, func(i, j int) bool { return st.Members[i].ID < st.Members[j].ID })
	return st
}

// CorruptLocal flips bits in this node's copy of an object (chaos testing).
func (n *Node) CorruptLocal(bucket, key string) (string, error) {
	m, err := n.meta.GetManifest(bucket, key)
	if err != nil {
		return "", err
	}
	if m == nil || m.Deleted {
		return "", ErrNotFound
	}
	for _, c := range m.Chunks {
		hashes := append([]string{c.Hash}, c.Shards...)
		for _, h := range hashes {
			if err := n.shards.CorruptForTesting(h); err == nil {
				n.Events.Publish("chaos.corrupt", "warning", bucket, key, "flipped bits in local shard "+h[:12]+"…")
				return h, nil
			}
		}
	}
	return "", errors.New("this node holds no shard of the object")
}

// DropLocal deletes this node's copy of an object's data (chaos testing: a
// lost replica / disk failure).
func (n *Node) DropLocal(bucket, key string) (int, error) {
	m, err := n.meta.GetManifest(bucket, key)
	if err != nil {
		return 0, err
	}
	if m == nil || m.Deleted {
		return 0, ErrNotFound
	}
	dropped := 0
	for _, c := range m.Chunks {
		for _, h := range append([]string{c.Hash}, c.Shards...) {
			if ok, _ := n.shards.Has(h, false); ok && n.shards.Delete(h) == nil {
				dropped++
			}
		}
	}
	n.Events.Publish("chaos.drop", "warning", bucket, key, fmt.Sprintf("deleted %d local shard(s)", dropped))
	return dropped, nil
}

// RepairNow enqueues an object for immediate repair.
func (n *Node) RepairNow(bucket, key string) {
	n.queue.enqueue(store.ObjectRef{Bucket: bucket, Key: key}, priorityCritical, "manual request")
}
