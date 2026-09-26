// Package node implements a Vault storage node: the coordinator for client
// requests (quorum writes and reads), the server side of the node-to-node
// API, and background maintenance (repair, scrubbing, anti-entropy,
// rebalancing and hinted handoff).
package node

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/catalog"
	"github.com/hydra-software/vault/engine/internal/events"
	"github.com/hydra-software/vault/engine/internal/hlc"
	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/metrics"
	"github.com/hydra-software/vault/engine/internal/policy"
	"github.com/hydra-software/vault/engine/internal/ring"
	"github.com/hydra-software/vault/engine/internal/store"
	"github.com/hydra-software/vault/engine/internal/transport"
)

// Errors returned to API clients.
var (
	ErrNotFound       = errors.New("object not found")
	ErrNoBucket       = errors.New("bucket not found")
	ErrBucketExists   = errors.New("bucket already exists")
	ErrBucketNotEmpty = errors.New("bucket is not empty")
	ErrUnavailable    = errors.New("not enough healthy replicas to satisfy the quorum")
	ErrTooLarge       = errors.New("object exceeds the maximum size")
)

// Config tunes a node. Zero values select defaults.
type Config struct {
	ID                  string
	Zone                string
	DataDir             string
	ChunkSize           int
	MaxObjectBytes      int64
	MaxBytes            int64
	EncryptionKey       []byte
	RPCTimeout          time.Duration
	HedgeDelay          time.Duration
	RepairWorkers       int
	ScrubInterval       time.Duration
	ScrubBytesPerSec    int64
	AntiEntropyInterval time.Duration
	RebalanceInterval   time.Duration
	GCGrace             time.Duration
	TombstoneTTL        time.Duration
	Logger              *slog.Logger
}

func (c *Config) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	if c.ChunkSize <= 0 {
		c.ChunkSize = 4 << 20
	}
	if c.MaxObjectBytes <= 0 {
		c.MaxObjectBytes = 64 << 30
	}
	if c.RepairWorkers <= 0 {
		c.RepairWorkers = 4
	}
	if c.ScrubBytesPerSec <= 0 {
		c.ScrubBytesPerSec = 32 << 20
	}
	def(&c.RPCTimeout, 10*time.Second)
	def(&c.HedgeDelay, 150*time.Millisecond)
	def(&c.ScrubInterval, 10*time.Minute)
	def(&c.AntiEntropyInterval, 30*time.Second)
	def(&c.RebalanceInterval, 2*time.Minute)
	def(&c.GCGrace, time.Hour)
	def(&c.TombstoneTTL, 24*time.Hour)
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Node is a single storage node.
type Node struct {
	cfg     Config
	log     *slog.Logger
	shards  *store.ShardStore
	meta    *store.Meta
	clock   *hlc.Clock
	catalog *catalog.Catalog
	members membership.Membership
	dialer  transport.Dialer
	faults  *transport.Faults
	Events  *events.Bus
	Metrics *metrics.Registry

	locks  [256]sync.Mutex
	viewMu sync.Mutex
	cached atomic.Pointer[view]
	queue  *repairQueue

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New opens local storage and wires a node. Call Start to begin background work.
func New(cfg Config, members membership.Membership, dialer transport.Dialer, faults *transport.Faults) (*Node, error) {
	cfg.defaults()
	if cfg.ID == "" || cfg.DataDir == "" {
		return nil, errors.New("node: ID and DataDir are required")
	}
	shards, err := store.OpenShards(filepath.Join(cfg.DataDir, "shards"), store.ShardOptions{MaxBytes: cfg.MaxBytes, EncryptionKey: cfg.EncryptionKey})
	if err != nil {
		return nil, err
	}
	meta, err := store.OpenMeta(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	cfgs, err := meta.LoadCatalog()
	if err != nil {
		_ = meta.Close()
		return nil, fmt.Errorf("node: load catalog: %w", err)
	}
	if faults == nil {
		faults = transport.NewFaults()
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{
		cfg: cfg, log: cfg.Logger.With("node", cfg.ID), shards: shards, meta: meta,
		clock: hlc.New(cfg.ID), catalog: catalog.New(cfgs, meta.SaveCatalog),
		members: members, dialer: transport.FaultyDialer{Inner: dialer, Self: cfg.ID, Faults: faults}, faults: faults,
		Events: events.NewBus(cfg.ID, 500), Metrics: metrics.NewRegistry(),
		ctx: ctx, cancel: cancel,
	}
	n.queue = newRepairQueue()
	return n, nil
}

// ID returns the node ID.
func (n *Node) ID() string { return n.cfg.ID }

// Faults exposes the node's fault injector (chaos API).
func (n *Node) Faults() *transport.Faults { return n.faults }

// Start launches background loops.
func (n *Node) Start() {
	for range n.cfg.RepairWorkers {
		n.goLoop(n.repairWorker)
	}
	n.goLoop(n.scrubLoop)
	n.goLoop(n.antiEntropyLoop)
	n.goLoop(n.rebalanceLoop)
	n.goLoop(n.watchMembership)
}

func (n *Node) watchMembership(ctx context.Context) {
	ch, cancel := n.members.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			n.OnMembershipChange()
		}
	}
}

func (n *Node) goLoop(fn func(ctx context.Context)) {
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		fn(n.ctx)
	}()
}

// Close stops background work and closes storage.
func (n *Node) Close() error {
	n.cancel()
	n.queue.close()
	n.wg.Wait()
	return n.meta.Close()
}

// view is a snapshot of membership plus the ring built from it.
type view struct {
	ring    *ring.Ring
	members map[string]membership.Member
}

func (n *Node) currentView() *view {
	if v := n.cached.Load(); v != nil {
		return v
	}
	n.viewMu.Lock()
	defer n.viewMu.Unlock()
	if v := n.cached.Load(); v != nil {
		return v
	}
	ms := n.members.Members()
	v := &view{members: make(map[string]membership.Member, len(ms))}
	rn := make([]ring.Node, 0, len(ms))
	for _, m := range ms {
		v.members[m.ID] = m
		rn = append(rn, ring.Node{ID: m.ID, Zone: m.Zone})
	}
	v.ring = ring.New(rn, ring.DefaultVNodes)
	n.cached.Store(v)
	return v
}

func (n *Node) invalidateView() { n.cached.Store(nil) }

func (v *view) preference(bucket, key string) []membership.Member {
	pref := v.ring.Preference(keys.ObjectHash(bucket, key))
	out := make([]membership.Member, len(pref))
	for i, p := range pref {
		out[i] = v.members[p.ID]
	}
	return out
}

func (v *view) alive() []membership.Member {
	out := make([]membership.Member, 0, len(v.members))
	for _, m := range v.members {
		if m.IsAlive() {
			out = append(out, m)
		}
	}
	return out
}

func (n *Node) lockFor(bucket, key string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(bucket))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return &n.locks[h.Sum32()%uint32(len(n.locks))]
}

func (n *Node) rpcCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, n.cfg.RPCTimeout)
}

// MaxObjectBytes is the largest upload this node will accept.
func (n *Node) MaxObjectBytes() int64 { return n.cfg.MaxObjectBytes }

func (n *Node) peer(m membership.Member) (transport.Peer, error) {
	if m.ID == n.cfg.ID {
		return n.selfPeer(), nil
	}
	return n.dialer.Dial(m)
}

// bucketPolicy resolves a bucket's policy from the catalog.
func (n *Node) bucketPolicy(bucket string) (*vaultpb.Policy, error) {
	b, ok := n.catalog.Get(bucket)
	if !ok {
		return nil, ErrNoBucket
	}
	return b.Policy, nil
}

// CreateBucket registers a bucket cluster-wide. Policies are immutable.
func (n *Node) CreateBucket(ctx context.Context, name string, p *vaultpb.Policy) (*vaultpb.BucketConfig, error) {
	if err := keys.ValidateBucket(name); err != nil {
		return nil, err
	}
	if err := policy.Validate(p); err != nil {
		return nil, err
	}
	if _, ok := n.catalog.Get(name); ok {
		return nil, ErrBucketExists
	}
	cfg := &vaultpb.BucketConfig{Name: name, Policy: p, Version: n.clock.Now(), CreatedUnixNano: time.Now().UnixNano()}
	if _, err := n.catalog.Merge(&vaultpb.Catalog{Buckets: []*vaultpb.BucketConfig{cfg}}); err != nil {
		return nil, err
	}
	n.pushCatalog(ctx)
	n.Events.Publish("bucket.created", events.Info, name, "", "bucket created with "+policy.Describe(p))
	return cfg, nil
}

// DeleteBucket removes an empty bucket.
func (n *Node) DeleteBucket(ctx context.Context, name string) error {
	cfg, ok := n.catalog.Get(name)
	if !ok {
		return ErrNoBucket
	}
	objs, _, err := n.List(ctx, name, "", "", 1)
	if err != nil {
		return err
	}
	if len(objs) > 0 {
		return ErrBucketNotEmpty
	}
	cfg.Deleted = true
	cfg.Version = n.clock.Now()
	if _, err := n.catalog.Merge(&vaultpb.Catalog{Buckets: []*vaultpb.BucketConfig{cfg}}); err != nil {
		return err
	}
	n.pushCatalog(ctx)
	n.Events.Publish("bucket.deleted", events.Info, name, "", "bucket deleted")
	return nil
}

// Buckets lists live buckets.
func (n *Node) Buckets() []*vaultpb.BucketConfig { return n.catalog.List() }

// EnsureBucket creates a bucket if it does not exist (used at bootstrap).
func (n *Node) EnsureBucket(ctx context.Context, name string, p *vaultpb.Policy) error {
	if _, ok := n.catalog.Get(name); ok {
		return nil
	}
	_, err := n.CreateBucket(ctx, name, p)
	if errors.Is(err, ErrBucketExists) {
		return nil
	}
	return err
}

func (n *Node) pushCatalog(ctx context.Context) {
	snap := n.catalog.Snapshot()
	for _, m := range n.currentView().alive() {
		if m.ID == n.cfg.ID {
			continue
		}
		p, err := n.peer(m)
		if err != nil {
			continue
		}
		cctx, cancel := n.rpcCtx(ctx)
		if remote, err := p.SyncCatalog(cctx, snap); err == nil {
			_, _ = n.catalog.Merge(remote)
		}
		cancel()
	}
}

// Catalog returns the catalog snapshot (used by join).
func (n *Node) Catalog() *vaultpb.Catalog { return n.catalog.Snapshot() }

// MergeCatalog merges a remote catalog (used by join).
func (n *Node) MergeCatalog(c *vaultpb.Catalog) error {
	_, err := n.catalog.Merge(c)
	return err
}

// OnMembershipChange must be called when membership changes.
func (n *Node) OnMembershipChange() {
	n.invalidateView()
	n.queue.kickRebalance()
}
