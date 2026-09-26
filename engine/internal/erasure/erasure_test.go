package erasure

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestRoundTripWithLostShards(t *testing.T) {
	data := make([]byte, 100_003)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	shards, err := Encode(data, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards) != 6 {
		t.Fatalf("got %d shards", len(shards))
	}
	shards[0], shards[4] = nil, nil
	out, err := Decode(shards, 4, 2, len(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("decoded data differs")
	}
	if shards[0] == nil || shards[4] == nil {
		t.Fatal("missing shards were not reconstructed")
	}
}

func TestTooFewShards(t *testing.T) {
	shards, err := Encode([]byte("hello world"), 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	shards[0], shards[1] = nil, nil
	if _, err := Decode(shards, 2, 1, 11); !errors.Is(err, ErrTooFewShards) {
		t.Fatalf("expected ErrTooFewShards, got %v", err)
	}
}

func TestEmptyInput(t *testing.T) {
	shards, err := Encode(nil, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(shards, 2, 1, 0)
	if err != nil || len(out) != 0 {
		t.Fatalf("got %v, %v", out, err)
	}
}
