// Package catalog holds bucket configurations, replicated to every node as a
// last-writer-wins map that is merged during anti-entropy.
package catalog

import (
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/hlc"
)

// Catalog is safe for concurrent use.
type Catalog struct {
	mu      sync.RWMutex
	buckets map[string]*vaultpb.BucketConfig
	persist func([]*vaultpb.BucketConfig) error
}

// New returns a catalog seeded with cfgs. persist is called with changed
// entries after every merge (may be nil).
func New(cfgs []*vaultpb.BucketConfig, persist func([]*vaultpb.BucketConfig) error) *Catalog {
	c := &Catalog{buckets: map[string]*vaultpb.BucketConfig{}, persist: persist}
	for _, b := range cfgs {
		c.buckets[b.Name] = b
	}
	return c
}

// Get returns a live bucket config.
func (c *Catalog) Get(name string) (*vaultpb.BucketConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.buckets[name]
	if !ok || b.Deleted {
		return nil, false
	}
	return proto.Clone(b).(*vaultpb.BucketConfig), true
}

// List returns live buckets sorted by name.
func (c *Catalog) List() []*vaultpb.BucketConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*vaultpb.BucketConfig, 0, len(c.buckets))
	for _, b := range c.buckets {
		if !b.Deleted {
			out = append(out, proto.Clone(b).(*vaultpb.BucketConfig))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snapshot returns every entry including tombstones.
func (c *Catalog) Snapshot() *vaultpb.Catalog {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := &vaultpb.Catalog{}
	for _, b := range c.buckets {
		out.Buckets = append(out.Buckets, proto.Clone(b).(*vaultpb.BucketConfig))
	}
	return out
}

// Merge applies newer entries from remote and reports whether anything changed.
func (c *Catalog) Merge(remote *vaultpb.Catalog) (bool, error) {
	if remote == nil {
		return false, nil
	}
	c.mu.Lock()
	var changed []*vaultpb.BucketConfig
	for _, b := range remote.Buckets {
		if cur, ok := c.buckets[b.Name]; ok && !hlc.Newer(b.Version, cur.Version) {
			continue
		}
		cp := proto.Clone(b).(*vaultpb.BucketConfig)
		c.buckets[b.Name] = cp
		changed = append(changed, cp)
	}
	c.mu.Unlock()
	if len(changed) > 0 && c.persist != nil {
		if err := c.persist(changed); err != nil {
			return true, err
		}
	}
	return len(changed) > 0, nil
}
