// Package store persists shards (content-addressed files) and metadata
// (a bbolt database) on a single node's local disk.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hydra-software/vault/engine/internal/keys"
)

// Errors returned by the shard store.
var (
	ErrNotFound = errors.New("shard not found")
	ErrCorrupt  = errors.New("shard failed integrity check")
	ErrQuota    = errors.New("node storage quota exceeded")
)

// ShardStore keeps each shard in its own file named by its SHA-256, which
// gives deduplication for free and makes path traversal impossible.
type ShardStore struct {
	dir      string
	maxBytes int64
	aead     cipher.AEAD
	used     atomic.Int64
}

// ShardOptions configures a ShardStore.
type ShardOptions struct {
	// MaxBytes is the quota for stored shard bytes; 0 means unlimited.
	MaxBytes int64
	// EncryptionKey enables AES-256-GCM encryption at rest when 32 bytes long.
	EncryptionKey []byte
}

// OpenShards opens (creating if needed) a shard directory.
func OpenShards(dir string, opts ShardOptions) (*ShardStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create shard dir: %w", err)
	}
	s := &ShardStore{dir: dir, maxBytes: opts.MaxBytes}
	if len(opts.EncryptionKey) > 0 {
		block, err := aes.NewCipher(opts.EncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("store: encryption key: %w", err)
		}
		if s.aead, err = cipher.NewGCM(block); err != nil {
			return nil, fmt.Errorf("store: gcm: %w", err)
		}
	}
	var total int64
	err := s.Walk(func(_ string, size int64, _ time.Time) error {
		total += size
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.used.Store(total)
	return s, nil
}

func (s *ShardStore) path(hash string) string {
	return filepath.Join(s.dir, hash[:2], hash)
}

// Put stores data under its hash after verifying the hash matches.
// It is idempotent: an existing intact copy is left untouched.
func (s *ShardStore) Put(hash string, data []byte) error {
	if err := keys.ValidateHash(hash); err != nil {
		return err
	}
	if keys.HashHex(data) != hash {
		return fmt.Errorf("%w: content does not match hash %s", ErrCorrupt, hash)
	}
	if ok, _ := s.Has(hash, false); ok {
		return nil
	}
	payload := data
	if s.aead != nil {
		nonce := make([]byte, s.aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("store: nonce: %w", err)
		}
		payload = s.aead.Seal(nonce, nonce, data, []byte(hash))
	}
	if s.maxBytes > 0 && s.used.Load()+int64(len(payload)) > s.maxBytes {
		return ErrQuota
	}
	final := s.path(hash)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return fmt.Errorf("store: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), "."+hash+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: temp file: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("store: write shard: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("store: fsync shard: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("store: close shard: %w", err)
	}
	prev, statErr := os.Stat(final)
	if err := os.Rename(tmp.Name(), final); err != nil {
		cleanup()
		return fmt.Errorf("store: commit shard: %w", err)
	}
	if statErr == nil {
		s.used.Add(-prev.Size())
	}
	s.used.Add(int64(len(payload)))
	return nil
}

// Get returns the verified plaintext of a shard. A shard that fails
// verification is removed so that it cannot be served again, and ErrCorrupt
// is returned.
func (s *ShardStore) Get(hash string) ([]byte, error) {
	if err := keys.ValidateHash(hash); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read shard: %w", err)
	}
	data, err := s.open(hash, raw)
	if err != nil {
		s.quarantine(hash, int64(len(raw)))
		return nil, err
	}
	return data, nil
}

func (s *ShardStore) open(hash string, raw []byte) ([]byte, error) {
	data := raw
	if s.aead != nil {
		ns := s.aead.NonceSize()
		if len(raw) < ns {
			return nil, ErrCorrupt
		}
		var err error
		data, err = s.aead.Open(nil, raw[:ns], raw[ns:], []byte(hash))
		if err != nil {
			return nil, ErrCorrupt
		}
	}
	if keys.HashHex(data) != hash {
		return nil, ErrCorrupt
	}
	return data, nil
}

func (s *ShardStore) quarantine(hash string, size int64) {
	if err := os.Remove(s.path(hash)); err == nil {
		s.used.Add(-size)
	}
}

// Has reports whether an intact shard exists. With verify=false only
// existence is checked; with verify=true the content is re-hashed.
func (s *ShardStore) Has(hash string, verify bool) (bool, error) {
	if err := keys.ValidateHash(hash); err != nil {
		return false, err
	}
	if !verify {
		_, err := os.Stat(s.path(hash))
		return err == nil, nil
	}
	_, err := s.Get(hash)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrCorrupt):
		return false, nil
	default:
		return false, err
	}
}

// Verify re-hashes a shard. It returns ErrCorrupt (and removes the file) when
// the content is damaged.
func (s *ShardStore) Verify(hash string) error {
	_, err := s.Get(hash)
	return err
}

// Delete removes a shard if present.
func (s *ShardStore) Delete(hash string) error {
	if err := keys.ValidateHash(hash); err != nil {
		return err
	}
	st, err := os.Stat(s.path(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: stat shard: %w", err)
	}
	if err := os.Remove(s.path(hash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: delete shard: %w", err)
	}
	s.used.Add(-st.Size())
	return nil
}

// Walk calls fn for every shard file. Temporary files are skipped.
func (s *ShardStore) Walk(fn func(hash string, size int64, mod time.Time) error) error {
	return filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") || keys.ValidateHash(d.Name()) != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // file removed concurrently
		}
		return fn(d.Name(), info.Size(), info.ModTime())
	})
}

// UsedBytes is the number of bytes currently stored on disk.
func (s *ShardStore) UsedBytes() int64 { return s.used.Load() }

// MaxBytes is the configured quota (0 = unlimited).
func (s *ShardStore) MaxBytes() int64 { return s.maxBytes }

// CorruptForTesting flips bytes in a stored shard to simulate bit rot.
// It is used by the chaos API and tests only.
func (s *ShardStore) CorruptForTesting(hash string) error {
	if err := keys.ValidateHash(hash); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(hash), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: open shard: %w", err)
	}
	defer f.Close()
	buf := make([]byte, 1)
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("store: read shard: %w", err)
	}
	buf[0] ^= 0xff
	if _, err := f.WriteAt(buf, 0); err != nil {
		return fmt.Errorf("store: corrupt shard: %w", err)
	}
	return nil
}
