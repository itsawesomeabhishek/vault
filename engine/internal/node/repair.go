// Background maintenance: priority repair, integrity scrub, Merkle
// anti-entropy, rebalance after ring changes, and hinted handoff.
package node

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"golang.org/x/time/rate"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/events"
	"github.com/hydra-software/vault/engine/internal/hlc"
	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/policy"
	"github.com/hydra-software/vault/engine/internal/ring"
	"github.com/hydra-software/vault/engine/internal/store"
)

const maxRepairAttempts = 20

func (n *Node) repairWorker(ctx context.Context) {
	for {
		it, ok := n.queue.pop()
		if !ok {
			return
		}
		res, err := n.repairObject(ctx, it.ref)
		n.queue.done(it.ref)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil || !res.complete:
			it.attempts++
			if it.attempts < maxRepairAttempts {
				backoff := time.Duration(1<<min(it.attempts, 5)) * 250 * time.Millisecond
				time.AfterFunc(backoff, func() { n.queue.push(it) })
			}
			if err != nil {
				n.log.Debug("repair incomplete", "bucket", it.ref.Bucket, "key", it.ref.Key, "err", err)
			}
		case res.fixed > 0:
			took := time.Since(it.queued)
			n.Metrics.Histogram("time_to_repair").Observe(took)
			n.Metrics.Counter("repairs_completed").Add(1)
			n.Events.Publish("repair.completed", events.Info, it.ref.Bucket, it.ref.Key,
				fmt.Sprintf("restored %d slot(s) in %s (%s)", res.fixed, took.Round(time.Millisecond), it.reason))
		}
	}
}

type repairResult struct {
	fixed    int
	complete bool
}

// repairObject makes every slot owner hold the newest manifest and all of its
// shards, rebuilding missing or corrupt data from healthy replicas (or by
// erasure decoding). Once all owners are healthy, a non-owner copy (stand-in
// or previous owner after rebalancing) is dropped.
func (n *Node) repairObject(ctx context.Context, ref store.ObjectRef) (repairResult, error) {
	local, err := n.meta.GetManifest(ref.Bucket, ref.Key)
	if err != nil {
		return repairResult{}, err
	}
	var p *vaultpb.Policy
	if local != nil {
		p = local.Policy
	} else if p, err = n.bucketPolicy(ref.Bucket); err != nil {
		return repairResult{complete: true}, nil
	}
	pref := n.currentView().preference(ref.Bucket, ref.Key)
	width := int(p.N)
	owners := pref[:min(width, len(pref))]

	versions := map[string]*vaultpb.Manifest{}
	newest := local
	for _, o := range owners {
		if !o.IsAlive() {
			continue
		}
		m, err := n.getManifestFrom(ctx, o, ref)
		if err != nil {
			continue
		}
		versions[o.ID] = m
		if m != nil && (newest == nil || hlc.Newer(m.Version, newest.Version)) {
			newest = m
		}
	}
	if newest == nil {
		return repairResult{complete: true}, nil
	}

	res := repairResult{}
	healthy := 0
	for slot, o := range owners {
		if !o.IsAlive() {
			continue
		}
		fixed, err := n.repairSlot(ctx, newest, slot, o, versions[o.ID])
		if err != nil {
			n.log.Debug("slot repair failed", "slot", slot, "owner", o.ID, "err", err)
			continue
		}
		if fixed {
			res.fixed++
		}
		healthy++
	}
	res.complete = healthy == width
	if !res.complete {
		return res, nil
	}
	selfOwner := false
	for _, o := range owners {
		selfOwner = selfOwner || o.ID == n.cfg.ID
	}
	_ = n.meta.DeleteHintsFor(ref.Bucket, ref.Key)
	if !selfOwner && local != nil && !hlc.Newer(local.Version, newest.Version) {
		if dropped, _ := n.meta.DropManifest(ref.Bucket, ref.Key, local.Version); dropped {
			n.Metrics.Counter("handoffs_completed").Add(1)
			n.Events.Publish("handoff.completed", events.Info, ref.Bucket, ref.Key, "data handed to its owners; local copy released")
		}
	}
	return res, nil
}

func (n *Node) getManifestFrom(ctx context.Context, m membership.Member, ref store.ObjectRef) (*vaultpb.Manifest, error) {
	p, err := n.peer(m)
	if err != nil {
		return nil, err
	}
	cctx, cancel := n.rpcCtx(ctx)
	defer cancel()
	return p.GetManifest(cctx, ref.Bucket, ref.Key)
}

// repairSlot ensures owner o holds the manifest and every shard for slot.
func (n *Node) repairSlot(ctx context.Context, man *vaultpb.Manifest, slot int, o membership.Member, have *vaultpb.Manifest) (bool, error) {
	peer, err := n.peer(o)
	if err != nil {
		return false, err
	}
	fixed := false
	if !man.Deleted {
		hashes := make([]string, len(man.Chunks))
		for i, c := range man.Chunks {
			hashes[i] = policy.ShardHash(man.Policy, c, slot)
		}
		cctx, cancel := n.rpcCtx(ctx)
		present, err := peer.HasShards(cctx, hashes, false)
		cancel()
		if err != nil {
			return false, err
		}
		for i, ok := range present {
			if ok {
				continue
			}
			data, err := n.shardData(ctx, man, i, slot)
			if err != nil {
				return false, err
			}
			cctx, cancel := n.rpcCtx(ctx)
			err = peer.PutShard(cctx, hashes[i], data)
			cancel()
			if err != nil {
				return false, err
			}
			n.Metrics.Counter("shards_repaired").Add(1)
			fixed = true
		}
	}
	if have == nil || hlc.Newer(man.Version, have.Version) {
		cctx, cancel := n.rpcCtx(ctx)
		_, err := peer.PutManifest(cctx, man, "")
		cancel()
		if err != nil {
			return false, err
		}
		fixed = true
	}
	return fixed, nil
}

// shardData returns the verified bytes of chunk ci for slot.
func (n *Node) shardData(ctx context.Context, man *vaultpb.Manifest, ci, slot int) ([]byte, error) {
	c := man.Chunks[ci]
	if man.Policy.K <= 1 {
		return n.fetchVerified(ctx, man, n.allAlive(), c.Hash)
	}
	shards, err := n.fetchShards(ctx, man, c, slot)
	if err != nil {
		return nil, err
	}
	if shards[slot] == nil {
		if err := erasureReconstruct(shards, man.Policy); err != nil {
			return nil, err
		}
	}
	if keys.HashHex(shards[slot]) != c.Shards[slot] {
		return nil, errors.New("reconstructed shard failed checksum")
	}
	return shards[slot], nil
}

func (n *Node) allAlive() []membership.Member {
	alive := n.currentView().alive()
	out := make([]membership.Member, 0, len(alive))
	for _, m := range alive {
		if m.ID == n.cfg.ID {
			out = append([]membership.Member{m}, out...)
		} else {
			out = append(out, m)
		}
	}
	return out
}

// scrubLoop periodically re-verifies every local shard at a bounded byte
// rate, garbage-collects unreferenced shards and old tombstones, and checks
// that this node holds every shard it owns.
func (n *Node) scrubLoop(ctx context.Context) {
	t := time.NewTicker(n.cfg.ScrubInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.Scrub(ctx)
		}
	}
}

// ScrubReport summarises one scrub pass.
type ScrubReport struct {
	Verified   int `json:"verified"`
	Corrupt    int `json:"corrupt"`
	Collected  int `json:"collected"`
	Missing    int `json:"missing"`
	Tombstones int `json:"tombstonesPurged"`
}

// Scrub runs one verification pass.
func (n *Node) Scrub(ctx context.Context) ScrubReport {
	var rep ScrubReport
	lim := rate.NewLimiter(rate.Limit(n.cfg.ScrubBytesPerSec), int(max(n.cfg.ScrubBytesPerSec, int64(n.cfg.ChunkSize)*2)))
	cutoff := time.Now().Add(-n.cfg.GCGrace)
	_ = n.shards.Walk(func(hash string, size int64, mod time.Time) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !n.meta.HasRefs(hash) {
			if mod.Before(cutoff) {
				if n.shards.Delete(hash) == nil {
					rep.Collected++
					n.Metrics.Counter("shards_collected").Add(1)
				}
			}
			return nil
		}
		_ = lim.WaitN(ctx, int(min(size, int64(lim.Burst()))))
		if err := n.shards.Verify(hash); errors.Is(err, store.ErrCorrupt) {
			rep.Corrupt++
			n.onCorruptShard(hash, "scrub")
		} else if err == nil {
			rep.Verified++
		}
		return nil
	})
	rep.Missing = n.checkOwnedShards()
	if purged, err := n.meta.PurgeTombstones(time.Now().Add(-n.cfg.TombstoneTTL)); err == nil {
		rep.Tombstones = purged
	}
	n.Metrics.Counter("scrub_passes").Add(1)
	n.Events.Publish("scrub.completed", events.Info, "", "", fmt.Sprintf("verified %d shards, %d corrupt, %d missing, %d collected", rep.Verified, rep.Corrupt, rep.Missing, rep.Collected))
	return rep
}

func (n *Node) checkOwnedShards() int {
	type want struct {
		ref    store.ObjectRef
		hashes []string
	}
	var wants []want
	v := n.currentView()
	_ = n.meta.ForEachManifest(func(m *vaultpb.Manifest) error {
		if m.Deleted {
			return nil
		}
		pref := v.preference(m.Bucket, m.Key)
		for slot := 0; slot < int(m.Policy.N) && slot < len(pref); slot++ {
			if pref[slot].ID != n.cfg.ID {
				continue
			}
			w := want{ref: store.ObjectRef{Bucket: m.Bucket, Key: m.Key}}
			for _, c := range m.Chunks {
				w.hashes = append(w.hashes, policy.ShardHash(m.Policy, c, slot))
			}
			wants = append(wants, w)
		}
		return nil
	})
	missing := 0
	for _, w := range wants {
		for _, h := range w.hashes {
			if ok, _ := n.shards.Has(h, false); !ok {
				missing++
				n.queue.enqueue(w.ref, priorityCritical, "owned shard missing locally")
				break
			}
		}
	}
	return missing
}

// antiEntropyLoop compares Merkle trees with each healthy peer and schedules
// repair for keys whose versions differ; it also converges bucket catalogs.
func (n *Node) antiEntropyLoop(ctx context.Context) {
	for {
		jitter := time.Duration(rand.Int64N(int64(n.cfg.AntiEntropyInterval) / 4))
		select {
		case <-ctx.Done():
			return
		case <-time.After(n.cfg.AntiEntropyInterval + jitter):
			n.AntiEntropy(ctx)
		}
	}
}

// AntiEntropy runs one round against every healthy peer and returns the
// number of keys scheduled for repair.
func (n *Node) AntiEntropy(ctx context.Context) int {
	scheduled := 0
	for _, m := range n.currentView().alive() {
		if m.ID == n.cfg.ID {
			continue
		}
		scheduled += n.syncWith(ctx, m)
	}
	n.Metrics.Counter("anti_entropy_rounds").Add(1)
	if scheduled > 0 {
		n.Events.Publish("anti_entropy.diff", events.Info, "", "", fmt.Sprintf("Merkle comparison found %d divergent key(s)", scheduled))
	}
	return scheduled
}

func (n *Node) syncWith(ctx context.Context, m membership.Member) int {
	p, err := n.peer(m)
	if err != nil {
		return 0
	}
	cctx, cancel := n.rpcCtx(ctx)
	defer cancel()
	if remote, err := p.SyncCatalog(cctx, n.catalog.Snapshot()); err == nil {
		_, _ = n.catalog.Merge(remote)
	}
	remote, err := p.MerkleTree(cctx, n.cfg.ID)
	if err != nil {
		return 0
	}
	local, err := n.merkleLeaves(m.ID)
	if err != nil || len(remote) != len(local) {
		return 0
	}
	scheduled := 0
	for leaf := range local {
		if string(local[leaf]) == string(remote[leaf]) {
			continue
		}
		theirs, err := p.LeafEntries(cctx, n.cfg.ID, uint32(leaf))
		if err != nil {
			continue
		}
		mine, _ := n.leafEntries(m.ID, uint32(leaf))
		idx := map[store.ObjectRef]*vaultpb.Version{}
		for _, e := range mine {
			idx[store.ObjectRef{Bucket: e.Bucket, Key: e.Key}] = e.Version
		}
		seen := map[store.ObjectRef]bool{}
		for _, e := range theirs {
			ref := store.ObjectRef{Bucket: e.Bucket, Key: e.Key}
			seen[ref] = true
			if hlc.Compare(idx[ref], e.Version) != 0 {
				n.queue.enqueue(ref, priorityDegraded, "anti-entropy")
				scheduled++
			}
		}
		for ref := range idx {
			if !seen[ref] {
				n.queue.enqueue(ref, priorityDegraded, "anti-entropy")
				scheduled++
			}
		}
	}
	return scheduled
}

// rebalanceLoop reacts to membership changes: objects whose owner set changed
// are re-replicated to their new owners and released by old owners, and data
// held for recovered nodes is handed back (hinted handoff).
func (n *Node) rebalanceLoop(ctx context.Context) {
	prev := n.currentView().ring
	t := time.NewTicker(n.cfg.RebalanceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.queue.rebalance:
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return
			}
		case <-t.C:
			prev = nil
		}
		prev = n.Rebalance(prev)
	}
}

// Rebalance schedules repair for objects affected by ring changes since prev
// (all misplaced objects if prev is nil) and returns the current ring.
func (n *Node) Rebalance(prev *ring.Ring) *ring.Ring {
	n.invalidateView()
	v := n.currentView()
	moved := 0
	_ = n.meta.ForEachManifest(func(m *vaultpb.Manifest) error {
		h := keys.ObjectHash(m.Bucket, m.Key)
		w := int(m.Policy.N)
		now := v.ring.Owners(h, w)
		selfOwner := false
		for _, o := range now {
			selfOwner = selfOwner || o.ID == n.cfg.ID
		}
		changed := !selfOwner
		if prev != nil && !changed {
			before := prev.Owners(h, w)
			changed = len(before) != len(now)
			for i := 0; !changed && i < len(now); i++ {
				changed = before[i].ID != now[i].ID
			}
		}
		if changed {
			n.queue.enqueue(store.ObjectRef{Bucket: m.Bucket, Key: m.Key}, priorityBackground, "rebalance")
			moved++
		}
		return nil
	})
	for _, id := range n.meta.HintNodes() {
		if mem, ok := v.members[id]; ok && mem.IsAlive() {
			refs, _ := n.meta.Hints(id)
			for _, r := range refs {
				n.queue.enqueue(r, priorityDegraded, "hinted handoff to "+id)
			}
		}
	}
	if moved > 0 {
		n.Metrics.Counter("rebalance_moves").Add(uint64(moved))
		n.Events.Publish("rebalance.scheduled", events.Info, "", "", fmt.Sprintf("%d object(s) need to move after a membership change", moved))
	}
	return v.ring
}
