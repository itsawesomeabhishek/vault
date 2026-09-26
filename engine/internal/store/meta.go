package store

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/hlc"
)

var (
	bktManifests = []byte("manifests")
	bktRefs      = []byte("refs")
	bktHints     = []byte("hints")
	bktCatalog   = []byte("catalog")
	bktState     = []byte("state")
)

// ObjectRef identifies an object.
type ObjectRef struct {
	Bucket string
	Key    string
}

// Meta is the node-local metadata index, stored in bbolt with fsync on every
// commit so that acknowledged manifests survive crashes.
type Meta struct {
	db *bolt.DB
}

// OpenMeta opens the metadata database in dir.
func OpenMeta(dir string) (*Meta, error) {
	db, err := bolt.Open(filepath.Join(dir, "meta.db"), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open meta.db: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bktManifests, bktRefs, bktHints, bktCatalog, bktState} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: init meta.db: %w", err)
	}
	return &Meta{db: db}, nil
}

// Close closes the database.
func (m *Meta) Close() error { return m.db.Close() }

func objKey(bucket, key string) []byte {
	b := make([]byte, 0, len(bucket)+1+len(key))
	b = append(b, bucket...)
	b = append(b, 0)
	return append(b, key...)
}

func splitObjKey(k []byte) ObjectRef {
	i := bytes.IndexByte(k, 0)
	if i < 0 {
		return ObjectRef{Key: string(k)}
	}
	return ObjectRef{Bucket: string(k[:i]), Key: string(k[i+1:])}
}

func refKey(hash, bucket, key string) []byte {
	return append(append([]byte(hash), 0), objKey(bucket, key)...)
}

func hintKey(node, bucket, key string) []byte {
	return append(append([]byte(node), 0), objKey(bucket, key)...)
}

func shardHashes(man *vaultpb.Manifest) map[string]struct{} {
	out := map[string]struct{}{}
	for _, c := range man.GetChunks() {
		out[c.Hash] = struct{}{}
		for _, s := range c.Shards {
			out[s] = struct{}{}
		}
	}
	return out
}

// PutManifest stores man if it is newer than the local copy (last writer
// wins by HLC version). It returns whether the manifest was applied.
// hintFor records that this node holds the object on behalf of another node.
func (m *Meta) PutManifest(man *vaultpb.Manifest, hintFor string) (bool, error) {
	data, err := proto.Marshal(man)
	if err != nil {
		return false, fmt.Errorf("store: marshal manifest: %w", err)
	}
	applied := false
	err = m.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bktManifests)
		k := objKey(man.Bucket, man.Key)
		var prev *vaultpb.Manifest
		if raw := mb.Get(k); raw != nil {
			prev = &vaultpb.Manifest{}
			if err := proto.Unmarshal(raw, prev); err != nil {
				return fmt.Errorf("decode manifest: %w", err)
			}
			if !hlc.Newer(man.Version, prev.Version) {
				return putHint(tx, hintFor, man)
			}
		}
		if err := mb.Put(k, data); err != nil {
			return err
		}
		refs := tx.Bucket(bktRefs)
		if prev != nil {
			for h := range shardHashes(prev) {
				if err := refs.Delete(refKey(h, prev.Bucket, prev.Key)); err != nil {
					return err
				}
			}
		}
		for h := range shardHashes(man) {
			if err := refs.Put(refKey(h, man.Bucket, man.Key), nil); err != nil {
				return err
			}
		}
		applied = true
		return putHint(tx, hintFor, man)
	})
	return applied, err
}

func putHint(tx *bolt.Tx, hintFor string, man *vaultpb.Manifest) error {
	if hintFor == "" {
		return nil
	}
	return tx.Bucket(bktHints).Put(hintKey(hintFor, man.Bucket, man.Key), nil)
}

// GetManifest returns the local manifest or nil if unknown.
func (m *Meta) GetManifest(bucket, key string) (*vaultpb.Manifest, error) {
	var out *vaultpb.Manifest
	err := m.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bktManifests).Get(objKey(bucket, key))
		if raw == nil {
			return nil
		}
		out = &vaultpb.Manifest{}
		return proto.Unmarshal(raw, out)
	})
	if err != nil {
		return nil, fmt.Errorf("store: get manifest: %w", err)
	}
	return out, nil
}

// DropManifest removes the local copy of an object if its version still
// equals ifVersion (so a concurrent newer write is never lost).
func (m *Meta) DropManifest(bucket, key string, ifVersion *vaultpb.Version) (bool, error) {
	dropped := false
	err := m.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bktManifests)
		k := objKey(bucket, key)
		raw := mb.Get(k)
		if raw == nil {
			return nil
		}
		prev := &vaultpb.Manifest{}
		if err := proto.Unmarshal(raw, prev); err != nil {
			return err
		}
		if hlc.Compare(prev.Version, ifVersion) != 0 {
			return nil
		}
		if err := mb.Delete(k); err != nil {
			return err
		}
		refs := tx.Bucket(bktRefs)
		for h := range shardHashes(prev) {
			if err := refs.Delete(refKey(h, bucket, key)); err != nil {
				return err
			}
		}
		dropped = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: drop manifest: %w", err)
	}
	return dropped, nil
}

// ListManifests returns manifests in key order for a bucket and prefix.
func (m *Meta) ListManifests(bucket, prefix, startAfter string, limit int) ([]*vaultpb.Manifest, error) {
	var out []*vaultpb.Manifest
	err := m.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktManifests).Cursor()
		base := objKey(bucket, prefix)
		seek := base
		if startAfter != "" && startAfter >= prefix {
			seek = append(objKey(bucket, startAfter), 0)
		}
		for k, v := c.Seek(seek); k != nil && bytes.HasPrefix(k, base); k, v = c.Next() {
			man := &vaultpb.Manifest{}
			if err := proto.Unmarshal(v, man); err != nil {
				return err
			}
			out = append(out, man)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: list manifests: %w", err)
	}
	return out, nil
}

// ForEachManifest calls fn for every local manifest inside a read
// transaction. fn must not block on I/O; copy what you need and return.
func (m *Meta) ForEachManifest(fn func(man *vaultpb.Manifest) error) error {
	return m.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bktManifests).ForEach(func(_, v []byte) error {
			man := &vaultpb.Manifest{}
			if err := proto.Unmarshal(v, man); err != nil {
				return err
			}
			return fn(man)
		})
	})
}

// ShardRefs lists the objects whose local manifest references a shard hash.
func (m *Meta) ShardRefs(hash string) ([]ObjectRef, error) {
	var out []ObjectRef
	err := m.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktRefs).Cursor()
		p := append([]byte(hash), 0)
		for k, _ := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
			out = append(out, splitObjKey(k[len(p):]))
		}
		return nil
	})
	return out, err
}

// Hints returns objects held on behalf of the given node.
func (m *Meta) Hints(node string) ([]ObjectRef, error) {
	var out []ObjectRef
	err := m.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktHints).Cursor()
		p := append([]byte(node), 0)
		for k, _ := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
			out = append(out, splitObjKey(k[len(p):]))
		}
		return nil
	})
	return out, err
}

// HintCount returns the number of outstanding hints.
func (m *Meta) HintCount() int {
	n := 0
	_ = m.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bktHints).Stats().KeyN
		return nil
	})
	return n
}

// DeleteHint removes a hint once the owner has the data.
func (m *Meta) DeleteHint(node, bucket, key string) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktHints).Delete(hintKey(node, bucket, key))
	})
}

// DeleteHintsFor removes every hint for any node matching the object.
func (m *Meta) DeleteHintsFor(bucket, key string) error {
	suffix := append([]byte{0}, objKey(bucket, key)...)
	return m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktHints)
		var del [][]byte
		err := b.ForEach(func(k, _ []byte) error {
			if bytes.HasSuffix(k, suffix) && !bytes.Contains(k[:len(k)-len(suffix)], []byte{0}) {
				del = append(del, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range del {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// PurgeTombstones deletes tombstones whose version is older than cutoff and
// returns how many were removed.
func (m *Meta) PurgeTombstones(cutoff time.Time) (int, error) {
	n := 0
	err := m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktManifests)
		var del [][]byte
		err := b.ForEach(func(k, v []byte) error {
			man := &vaultpb.Manifest{}
			if err := proto.Unmarshal(v, man); err != nil {
				return err
			}
			if man.Deleted && man.GetVersion().GetWall() < cutoff.UnixNano() {
				del = append(del, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, k := range del {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		n = len(del)
		return nil
	})
	return n, err
}

// Counts returns the number of live objects and tombstones.
func (m *Meta) Counts() (live, tombstones int, logicalBytes uint64) {
	_ = m.ForEachManifest(func(man *vaultpb.Manifest) error {
		if man.Deleted {
			tombstones++
		} else {
			live++
			logicalBytes += man.Size
		}
		return nil
	})
	return live, tombstones, logicalBytes
}

// SaveCatalog persists bucket configurations.
func (m *Meta) SaveCatalog(cfgs []*vaultpb.BucketConfig) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktCatalog)
		for _, c := range cfgs {
			data, err := proto.Marshal(c)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(c.Name), data); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadCatalog reads every persisted bucket configuration.
func (m *Meta) LoadCatalog() ([]*vaultpb.BucketConfig, error) {
	var out []*vaultpb.BucketConfig
	err := m.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bktCatalog).ForEach(func(_, v []byte) error {
			c := &vaultpb.BucketConfig{}
			if err := proto.Unmarshal(v, c); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	})
	return out, err
}

// ErrNoState is returned by GetState for missing keys.
var ErrNoState = errors.New("state key not found")

// GetState reads a small persisted value.
func (m *Meta) GetState(key string) ([]byte, error) {
	var out []byte
	err := m.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bktState).Get([]byte(key))
		if v == nil {
			return ErrNoState
		}
		out = append([]byte(nil), v...)
		return nil
	})
	return out, err
}

// PutState writes a small persisted value.
func (m *Meta) PutState(key string, value []byte) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktState).Put([]byte(key), value)
	})
}

// HasRefs reports whether any local manifest references the shard.
func (m *Meta) HasRefs(hash string) bool {
	found := false
	_ = m.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bktRefs).Cursor()
		p := append([]byte(hash), 0)
		k, _ := c.Seek(p)
		found = k != nil && bytes.HasPrefix(k, p)
		return nil
	})
	return found
}

// HintNodes lists nodes that have outstanding hints.
func (m *Meta) HintNodes() []string {
	seen := map[string]struct{}{}
	_ = m.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bktHints).ForEach(func(k, _ []byte) error {
			if i := bytes.IndexByte(k, 0); i > 0 {
				seen[string(k[:i])] = struct{}{}
			}
			return nil
		})
	})
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	return out
}

// IsSystemKey reports whether a key belongs to the reserved namespace.
func IsSystemKey(key string) bool { return strings.HasPrefix(key, "__vault/") }
