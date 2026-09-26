package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/erasure"
	"github.com/hydra-software/vault/engine/internal/events"
	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/store"
)

// target is where one slot of an object is written.
type target struct {
	member  membership.Member
	hintFor string
	ok      bool
}

// writeTargets picks one node per slot. Slot i belongs to the i-th node of the
// preference list; when that node is down and the policy allows a sloppy
// quorum, the next healthy node outside the owner set stands in and records a
// hint so the data is handed back when the owner returns.
func (n *Node) writeTargets(v *view, p *vaultpb.Policy, bucket, key string) []target {
	pref := v.preference(bucket, key)
	width := int(p.N)
	out := make([]target, width)
	used := map[string]bool{}
	next := width
	for i := range width {
		if i < len(pref) && pref[i].IsAlive() {
			out[i] = target{member: pref[i], ok: true}
			used[pref[i].ID] = true
		}
	}
	if !p.Sloppy {
		return out
	}
	for i := range width {
		if out[i].ok {
			continue
		}
		for next < len(pref) && (!pref[next].IsAlive() || used[pref[next].ID]) {
			next++
		}
		if next >= len(pref) {
			break
		}
		hint := ""
		if i < len(pref) {
			hint = pref[i].ID
		}
		out[i] = target{member: pref[next], hintFor: hint, ok: true}
		used[pref[next].ID] = true
		next++
	}
	return out
}

func countOK(ts []target) int {
	c := 0
	for _, t := range ts {
		if t.ok {
			c++
		}
	}
	return c
}

type chunkData struct {
	data []byte
	buf  []byte
	err  error
}

var chunkPool = sync.Pool{New: func() any { return make([]byte, 4<<20) }}

func allocChunk(size int) []byte {
	if size == 4<<20 {
		return chunkPool.Get().([]byte)
	}
	return make([]byte, size)
}

func recycleChunk(buf []byte) {
	if buf != nil && cap(buf) == 4<<20 {
		chunkPool.Put(buf[:4<<20])
	}
}

// Put stores an object read from r and returns its manifest once W slots
// have durably stored every chunk and the manifest.
func (n *Node) Put(ctx context.Context, bucket, key string, r io.Reader, contentType string, meta map[string]string) (*vaultpb.Manifest, error) {
	start := time.Now()
	if err := keys.ValidateBucket(bucket); err != nil {
		return nil, err
	}
	if err := keys.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := keys.ValidateMetadata(meta); err != nil {
		return nil, err
	}
	p, err := n.bucketPolicy(bucket)
	if err != nil {
		return nil, err
	}
	mu := n.lockFor(bucket, key)
	mu.Lock()
	defer mu.Unlock()

	targets := n.writeTargets(n.currentView(), p, bucket, key)
	if countOK(targets) < int(p.W) {
		n.Metrics.Counter("put_unavailable").Add(1)
		return nil, fmt.Errorf("%w: %d of %d required nodes reachable", ErrUnavailable, countOK(targets), p.W)
	}

	chunks := make(chan chunkData, 1)
	rctx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	go n.readChunks(rctx, r, chunks)

	whole := sha256.New()
	var size uint64
	man := &vaultpb.Manifest{Bucket: bucket, Key: key, ContentType: contentType, Metadata: meta, Policy: p, CreatedUnixNano: time.Now().UnixNano()}
	for c := range chunks {
		if c.err != nil {
			return nil, c.err
		}
		size += uint64(len(c.data))
		if int64(size) > n.cfg.MaxObjectBytes {
			return nil, ErrTooLarge
		}
		whole.Write(c.data)
		chunk, err := n.writeChunk(ctx, p, targets, c.data)
		recycleChunk(c.buf)
		if err != nil {
			return nil, err
		}
		man.Chunks = append(man.Chunks, chunk)
	}
	man.Size = size
	man.Etag = hex.EncodeToString(whole.Sum(nil))
	man.Version = n.clock.Now()
	if err := n.commitManifest(ctx, p, targets, man); err != nil {
		return nil, err
	}
	n.Metrics.Histogram("put_latency").Observe(time.Since(start))
	n.Metrics.Counter("put_ok").Add(1)
	n.Metrics.Counter("put_bytes").Add(size)
	n.Events.Publish("object.put", events.Info, bucket, key, fmt.Sprintf("stored %d bytes on %d/%d slots", size, countOK(targets), p.N))
	return man, nil
}

func (n *Node) readChunks(ctx context.Context, r io.Reader, out chan<- chunkData) {
	defer close(out)
	for {
		buf := allocChunk(n.cfg.ChunkSize)
		k, err := io.ReadFull(r, buf)
		if k > 0 {
			select {
			case out <- chunkData{data: buf[:k], buf: buf}:
			case <-ctx.Done():
				return
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
		if err != nil {
			select {
			case out <- chunkData{err: fmt.Errorf("read upload: %w", err)}:
			case <-ctx.Done():
			}
			return
		}
	}
}

// writeChunk sends one chunk's shards to every healthy slot in parallel.
// Slots that fail are excluded from the rest of the object.
func (n *Node) writeChunk(ctx context.Context, p *vaultpb.Policy, targets []target, data []byte) (*vaultpb.Chunk, error) {
	chunk := &vaultpb.Chunk{Hash: keys.HashHex(data), Size: uint64(len(data))}
	shards := make([][]byte, p.N)
	if p.K > 1 {
		enc, err := erasure.Encode(data, int(p.K), int(p.N-p.K))
		if err != nil {
			return nil, err
		}
		shards = enc
		chunk.Shards = make([]string, len(enc))
		for i, s := range enc {
			chunk.Shards[i] = keys.HashHex(s)
		}
	} else {
		for i := range shards {
			shards[i] = data
		}
	}
	var wg sync.WaitGroup
	for i := range targets {
		if !targets[i].ok {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hash := chunk.Hash
			if p.K > 1 {
				hash = chunk.Shards[i]
			}
			if err := n.sendShard(ctx, targets[i].member, hash, shards[i]); err != nil {
				n.log.Warn("shard write failed", "slot", i, "target", targets[i].member.ID, "err", err)
				targets[i].ok = false
			}
		}(i)
	}
	wg.Wait()
	if countOK(targets) < int(p.W) {
		n.Metrics.Counter("put_unavailable").Add(1)
		return nil, fmt.Errorf("%w: only %d of %d required slots stored a chunk", ErrUnavailable, countOK(targets), p.W)
	}
	return chunk, nil
}

func (n *Node) sendShard(ctx context.Context, m membership.Member, hash string, data []byte) error {
	p, err := n.peer(m)
	if err != nil {
		return err
	}
	cctx, cancel := n.rpcCtx(ctx)
	defer cancel()
	return p.PutShard(cctx, hash, data)
}

// commitManifest writes the manifest to every slot that holds all chunks and
// returns after W acknowledgements. Remaining writes finish in the background.
func (n *Node) commitManifest(ctx context.Context, p *vaultpb.Policy, targets []target, man *vaultpb.Manifest) error {
	results := make(chan bool, len(targets))
	sent := 0
	for i := range targets {
		if !targets[i].ok {
			continue
		}
		sent++
		t := targets[i]
		go func() {
			bctx, cancel := n.rpcCtx(n.ctx)
			defer cancel()
			peer, err := n.peer(t.member)
			if err == nil {
				_, err = peer.PutManifest(bctx, man, t.hintFor)
			}
			if err != nil {
				n.log.Warn("manifest write failed", "target", t.member.ID, "err", err)
			}
			results <- err == nil
		}()
	}
	acks, done := 0, 0
	for acks < int(p.W) && done < sent {
		select {
		case ok := <-results:
			done++
			if ok {
				acks++
			}
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		}
	}
	if acks < int(p.W) {
		n.Metrics.Counter("put_unavailable").Add(1)
		return fmt.Errorf("%w: manifest acknowledged by %d of %d required slots", ErrUnavailable, acks, p.W)
	}
	degraded := countOK(targets) < int(p.N)
	for _, t := range targets {
		degraded = degraded || t.hintFor != ""
	}
	if degraded {
		n.queue.enqueueAfter(store.ObjectRef{Bucket: man.Bucket, Key: man.Key}, priorityDegraded, "under-replicated write", time.Second)
	}
	return nil
}

// Delete writes a tombstone with the same quorum rules as Put.
func (n *Node) Delete(ctx context.Context, bucket, key string) error {
	if err := keys.ValidateKey(key); err != nil {
		return err
	}
	p, err := n.bucketPolicy(bucket)
	if err != nil {
		return err
	}
	mu := n.lockFor(bucket, key)
	mu.Lock()
	defer mu.Unlock()
	cur, err := n.Head(ctx, bucket, key)
	if err != nil {
		return err
	}
	targets := n.writeTargets(n.currentView(), p, bucket, key)
	if countOK(targets) < int(p.W) {
		return fmt.Errorf("%w: %d of %d required nodes reachable", ErrUnavailable, countOK(targets), p.W)
	}
	n.clock.Observe(cur.Version)
	ts := &vaultpb.Manifest{Bucket: bucket, Key: key, Deleted: true, Policy: p, Version: n.clock.Now(), CreatedUnixNano: time.Now().UnixNano()}
	if err := n.commitManifest(ctx, p, targets, ts); err != nil {
		return err
	}
	n.Metrics.Counter("delete_ok").Add(1)
	n.Events.Publish("object.deleted", events.Info, bucket, key, "tombstone written")
	return nil
}
