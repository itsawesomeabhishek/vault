package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/erasure"
	"github.com/hydra-software/vault/engine/internal/hlc"
	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/policy"
	"github.com/hydra-software/vault/engine/internal/store"
	"github.com/hydra-software/vault/engine/internal/transport"
)

type manifestReply struct {
	member membership.Member
	m      *vaultpb.Manifest
	err    error
}

// readCandidates returns the owners plus healthy stand-ins, so reads still
// find data written with a sloppy quorum while owners are down.
func readCandidates(pref []membership.Member, width int) []membership.Member {
	var out []membership.Member
	alive := 0
	for i, m := range pref {
		if i < width {
			if m.IsAlive() {
				out = append(out, m)
				alive++
			}
			continue
		}
		if alive >= width {
			break
		}
		if m.IsAlive() {
			out = append(out, m)
			alive++
		}
	}
	return out
}

// readManifest asks the owners for the manifest, waits for R replies and
// returns the newest. Stale replicas, including ones replying late, are
// repaired in the background (read repair).
func (n *Node) readManifest(ctx context.Context, bucket, key string, p *vaultpb.Policy) (*vaultpb.Manifest, error) {
	cands := readCandidates(n.currentView().preference(bucket, key), int(p.N))
	if len(cands) < int(p.R) {
		n.Metrics.Counter("get_unavailable").Add(1)
		return nil, fmt.Errorf("%w: %d of %d required nodes reachable", ErrUnavailable, len(cands), p.R)
	}
	replies := make(chan manifestReply, len(cands))
	for _, m := range cands {
		go func(m membership.Member) {
			cctx, cancel := n.rpcCtx(n.ctx)
			defer cancel()
			peer, err := n.peer(m)
			var man *vaultpb.Manifest
			if err == nil {
				man, err = peer.GetManifest(cctx, bucket, key)
			}
			replies <- manifestReply{member: m, m: man, err: err}
		}(m)
	}
	var got []manifestReply
	okCount := 0
	for len(got) < len(cands) && okCount < int(p.R) {
		select {
		case r := <-replies:
			got = append(got, r)
			if r.err == nil {
				okCount++
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		}
	}
	if okCount < int(p.R) {
		n.Metrics.Counter("get_unavailable").Add(1)
		return nil, fmt.Errorf("%w: %d of %d required replicas answered", ErrUnavailable, okCount, p.R)
	}
	newest := newestOf(got)
	remaining := len(cands) - len(got)
	go n.readRepair(bucket, key, got, replies, remaining)
	return newest, nil
}

func newestOf(rs []manifestReply) *vaultpb.Manifest {
	var best *vaultpb.Manifest
	for _, r := range rs {
		if r.err == nil && r.m != nil && (best == nil || hlc.Newer(r.m.Version, best.Version)) {
			best = r.m
		}
	}
	return best
}

func (n *Node) readRepair(bucket, key string, got []manifestReply, more <-chan manifestReply, remaining int) {
	timeout := time.NewTimer(n.cfg.RPCTimeout)
	defer timeout.Stop()
collect:
	for ; remaining > 0; remaining-- {
		select {
		case r := <-more:
			got = append(got, r)
		case <-timeout.C:
			break collect
		case <-n.ctx.Done():
			return
		}
	}
	newest := newestOf(got)
	if newest == nil {
		return
	}
	stale := 0
	for _, r := range got {
		if r.err != nil || r.m == nil || hlc.Newer(newest.Version, r.m.Version) {
			stale++
		}
	}
	if stale > 0 {
		n.Metrics.Counter("read_repairs").Add(1)
		n.queue.enqueue(store.ObjectRef{Bucket: bucket, Key: key}, priorityDegraded, "read repair")
	}
}

// Head returns the newest live manifest of an object.
func (n *Node) Head(ctx context.Context, bucket, key string) (*vaultpb.Manifest, error) {
	if err := keys.ValidateKey(key); err != nil {
		return nil, err
	}
	p, err := n.bucketPolicy(bucket)
	if err != nil {
		return nil, err
	}
	m, err := n.readManifest(ctx, bucket, key, p)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Deleted {
		return nil, ErrNotFound
	}
	return m, nil
}

// Get returns the manifest and a stream of the object's verified bytes.
// Chunks are fetched ahead of the consumer to overlap network latency.
func (n *Node) Get(ctx context.Context, bucket, key string) (*vaultpb.Manifest, io.ReadCloser, error) {
	start := time.Now()
	m, err := n.Head(ctx, bucket, key)
	if err != nil {
		return nil, nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		const window = 3
		type result struct {
			data []byte
			err  error
		}
		futures := make([]chan result, len(m.Chunks))
		launch := func(i int) {
			futures[i] = make(chan result, 1)
			go func() {
				d, err := n.fetchChunk(ctx, m, i)
				futures[i] <- result{d, err}
			}()
		}
		for i := 0; i < len(m.Chunks) && i < window; i++ {
			launch(i)
		}
		whole := sha256.New()
		for i := range m.Chunks {
			r := <-futures[i]
			if i+window < len(m.Chunks) {
				launch(i + window)
			}
			if r.err != nil {
				n.Metrics.Counter("get_failed").Add(1)
				pw.CloseWithError(r.err)
				return
			}
			whole.Write(r.data)
			if _, err := pw.Write(r.data); err != nil {
				return
			}
		}
		if m.Etag != "" && hex.EncodeToString(whole.Sum(nil)) != m.Etag {
			pw.CloseWithError(errors.New("object checksum mismatch"))
			return
		}
		n.Metrics.Histogram("get_latency").Observe(time.Since(start))
		n.Metrics.Counter("get_ok").Add(1)
		pw.Close()
	}()
	return m, pr, nil
}

// holders orders nodes that may hold an object's data: local node first,
// then the preference list.
func (n *Node) holders(m *vaultpb.Manifest) []membership.Member {
	pref := n.currentView().preference(m.Bucket, m.Key)
	out := make([]membership.Member, 0, len(pref))
	for _, p := range pref {
		if p.ID == n.cfg.ID {
			out = append([]membership.Member{p}, out...)
		} else if p.IsAlive() {
			out = append(out, p)
		}
	}
	return out
}

func (n *Node) fetchChunk(ctx context.Context, m *vaultpb.Manifest, idx int) ([]byte, error) {
	c := m.Chunks[idx]
	if m.Policy.K <= 1 {
		return n.fetchVerified(ctx, m, n.holders(m), c.Hash)
	}
	shards, err := n.fetchShards(ctx, m, c, -1)
	if err != nil {
		return nil, err
	}
	data, err := erasure.Decode(shards, int(m.Policy.K), int(m.Policy.N-m.Policy.K), int(c.Size))
	if err != nil {
		return nil, err
	}
	if keys.HashHex(data) != c.Hash {
		return nil, fmt.Errorf("chunk %d failed checksum after decode", idx)
	}
	return data, nil
}

// fetchVerified downloads hash from the first candidate that returns intact
// data. After HedgeDelay without an answer, the next candidate is tried in
// parallel (hedged request) to bound tail latency.
func (n *Node) fetchVerified(ctx context.Context, m *vaultpb.Manifest, cands []membership.Member, hash string) ([]byte, error) {
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: no reachable replica", ErrUnavailable)
	}
	type res struct {
		data []byte
		from membership.Member
		err  error
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan res, len(cands))
	launched, failed := 0, 0
	launch := func() {
		mem := cands[launched]
		launched++
		go func() {
			rctx, rc := n.rpcCtx(cctx)
			defer rc()
			p, err := n.peer(mem)
			var d []byte
			if err == nil {
				d, err = p.GetShard(rctx, hash)
			}
			if err == nil && keys.HashHex(d) != hash {
				err = transport.ErrCorrupt
			}
			out <- res{d, mem, err}
		}()
	}
	launch()
	hedge := time.NewTimer(n.cfg.HedgeDelay)
	defer hedge.Stop()
	for {
		select {
		case r := <-out:
			if r.err == nil {
				return r.data, nil
			}
			failed++
			if errors.Is(r.err, transport.ErrCorrupt) || errors.Is(r.err, transport.ErrNotFound) {
				n.queue.enqueue(store.ObjectRef{Bucket: m.Bucket, Key: m.Key}, priorityCritical, "missing or corrupt replica on "+r.from.ID)
			}
			if launched < len(cands) {
				launch()
			} else if failed == launched {
				return nil, fmt.Errorf("%w: every replica of shard %s… failed", ErrUnavailable, hash[:12])
			}
		case <-hedge.C:
			if launched < len(cands) {
				n.Metrics.Counter("hedged_requests").Add(1)
				launch()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// fetchShards gathers at least K verified shards of an erasure-coded chunk.
// If want >= 0 it keeps fetching until that slot is available or rebuildable.
func (n *Node) fetchShards(ctx context.Context, m *vaultpb.Manifest, c *vaultpb.Chunk, want int) ([][]byte, error) {
	p := m.Policy
	pref := n.currentView().preference(m.Bucket, m.Key)
	shards := make([][]byte, p.N)
	type res struct {
		slot int
		data []byte
	}
	out := make(chan res, p.N)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for slot := range int(p.N) {
		go func(slot int) {
			hash := c.Shards[slot]
			var cands []membership.Member
			if slot < len(pref) && pref[slot].IsAlive() {
				cands = append(cands, pref[slot])
			}
			for _, h := range n.holders(m) {
				if slot >= len(pref) || h.ID != pref[slot].ID {
					cands = append(cands, h)
				}
			}
			for _, mem := range cands {
				rctx, rc := n.rpcCtx(cctx)
				peer, err := n.peer(mem)
				var d []byte
				if err == nil {
					d, err = peer.GetShard(rctx, hash)
				}
				rc()
				if err == nil && keys.HashHex(d) == hash {
					out <- res{slot, d}
					return
				}
				if slot < len(pref) && mem.ID == pref[slot].ID {
					n.queue.enqueue(store.ObjectRef{Bucket: m.Bucket, Key: m.Key}, priorityCritical, "missing shard on "+mem.ID)
				}
			}
			out <- res{slot, nil}
		}(slot)
	}
	have := 0
	for range int(p.N) {
		r := <-out
		if r.data != nil {
			shards[r.slot] = r.data
			have++
		}
		if have >= int(p.K) && (want < 0 || shards[want] != nil) {
			return shards, nil
		}
	}
	if have < int(p.K) {
		return nil, fmt.Errorf("%w: only %d of %d shards available", erasure.ErrTooFewShards, have, p.K)
	}
	return shards, nil
}

// ObjectSummary is a listing entry.
type ObjectSummary struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Size        uint64            `json:"size"`
	ContentType string            `json:"contentType"`
	Etag        string            `json:"etag"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Modified    time.Time         `json:"modified"`
	Policy      string            `json:"policy"`
}

// Summarize converts a manifest to a listing entry.
func Summarize(m *vaultpb.Manifest) ObjectSummary {
	return ObjectSummary{
		Bucket: m.Bucket, Key: m.Key, Size: m.Size, ContentType: m.ContentType, Etag: m.Etag, Metadata: m.Metadata,
		Modified: time.Unix(0, m.GetVersion().GetWall()).UTC(), Policy: policy.Describe(m.Policy),
	}
}

// List merges listings from every healthy node, keeping the newest version
// of each key and hiding tombstones. It returns a continuation token.
func (n *Node) List(ctx context.Context, bucket, prefix, startAfter string, limit int) ([]ObjectSummary, string, error) {
	if _, err := n.bucketPolicy(bucket); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	alive := n.currentView().alive()
	type res struct {
		list []*vaultpb.Manifest
		err  error
	}
	out := make(chan res, len(alive))
	for _, m := range alive {
		go func(m membership.Member) {
			cctx, cancel := n.rpcCtx(ctx)
			defer cancel()
			p, err := n.peer(m)
			var l []*vaultpb.Manifest
			if err == nil {
				l, err = p.ListManifests(cctx, &vaultpb.ListManifestsRequest{Bucket: bucket, Prefix: prefix, StartAfter: startAfter, Limit: uint32(limit + 1)})
			}
			out <- res{l, err}
		}(m)
	}
	newest := map[string]*vaultpb.Manifest{}
	answered := 0
	for range alive {
		r := <-out
		if r.err != nil {
			continue
		}
		answered++
		for _, m := range r.list {
			if cur, ok := newest[m.Key]; !ok || hlc.Newer(m.Version, cur.Version) {
				newest[m.Key] = m
			}
		}
	}
	if answered == 0 {
		return nil, "", fmt.Errorf("%w: no node answered the listing", ErrUnavailable)
	}
	keysSorted := make([]string, 0, len(newest))
	for k, m := range newest {
		if !m.Deleted {
			keysSorted = append(keysSorted, k)
		}
	}
	sort.Strings(keysSorted)
	next := ""
	if len(keysSorted) > limit {
		keysSorted = keysSorted[:limit]
		next = keysSorted[limit-1]
	}
	items := make([]ObjectSummary, 0, len(keysSorted))
	for _, k := range keysSorted {
		items = append(items, Summarize(newest[k]))
	}
	return items, next, nil
}
