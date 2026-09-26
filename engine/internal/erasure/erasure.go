// Package erasure wraps Reed-Solomon coding for chunk shards.
package erasure

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/reedsolomon"
)

// ErrTooFewShards is returned when fewer than k shards are available.
var ErrTooFewShards = errors.New("erasure: not enough shards to reconstruct")

type codecKey struct{ k, m int }

var (
	mu     sync.Mutex
	codecs = map[codecKey]reedsolomon.Encoder{}
)

func codec(k, m int) (reedsolomon.Encoder, error) {
	mu.Lock()
	defer mu.Unlock()
	key := codecKey{k, m}
	if enc, ok := codecs[key]; ok {
		return enc, nil
	}
	enc, err := reedsolomon.New(k, m)
	if err != nil {
		return nil, fmt.Errorf("erasure: new codec %d+%d: %w", k, m, err)
	}
	codecs[key] = enc
	return enc, nil
}

// Encode splits data into k data shards and m parity shards.
func Encode(data []byte, k, m int) ([][]byte, error) {
	enc, err := codec(k, m)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		data = []byte{0}
	}
	shards, err := enc.Split(data)
	if err != nil {
		return nil, fmt.Errorf("erasure: split: %w", err)
	}
	if err := enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("erasure: encode: %w", err)
	}
	return shards, nil
}

// Decode rebuilds the original data of the given size. Missing shards must be
// nil. The slice is modified in place: missing shards are reconstructed.
func Decode(shards [][]byte, k, m int, size int) ([]byte, error) {
	if err := Reconstruct(shards, k, m); err != nil {
		return nil, err
	}
	if size == 0 {
		return []byte{}, nil
	}
	enc, err := codec(k, m)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Grow(size)
	if err := enc.Join(&buf, shards, size); err != nil {
		return nil, fmt.Errorf("erasure: join: %w", err)
	}
	return buf.Bytes(), nil
}

// Reconstruct fills in every nil shard (data and parity).
func Reconstruct(shards [][]byte, k, m int) error {
	have := 0
	for _, s := range shards {
		if s != nil {
			have++
		}
	}
	if have < k {
		return ErrTooFewShards
	}
	enc, err := codec(k, m)
	if err != nil {
		return err
	}
	if err := enc.Reconstruct(shards); err != nil {
		return fmt.Errorf("erasure: reconstruct: %w", err)
	}
	return nil
}
