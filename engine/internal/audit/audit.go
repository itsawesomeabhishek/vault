// Package audit writes a tamper-evident, append-only access log. Each entry
// includes the SHA-256 of the previous entry, so editing or deleting a line
// breaks the chain and is detected by Verify.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Entry is one audited action.
type Entry struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Bucket string    `json:"bucket,omitempty"`
	Key    string    `json:"key,omitempty"`
	Result string    `json:"result"`
	Prev   string    `json:"prev"`
	Hash   string    `json:"hash"`
}

func (e Entry) digest() string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Log is safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	path string
	seq  uint64
	last string
	// Tampered is set to the quarantine path when Open found a broken chain.
	Tampered string
}

// ErrTampered is returned by Verify when the chain is broken.
var ErrTampered = errors.New("audit log chain broken")

// Open opens or creates the log, validating the existing chain.
func Open(path string) (*Log, error) {
	l := &Log{path: path}
	entries, err := read(path)
	if errors.Is(err, ErrTampered) {
		// Preserve the evidence and start a fresh chain.
		l.Tampered = path + ".tampered-" + time.Now().UTC().Format("20060102T150405")
		if rerr := os.Rename(path, l.Tampered); rerr != nil {
			return nil, fmt.Errorf("audit: quarantine tampered log: %w", rerr)
		}
		entries, err = nil, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		l.seq, l.last = last.Seq, last.Hash
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open: %w", err)
	}
	l.f = f
	return l, nil
}

// Record appends an entry.
func (l *Log) Record(actor, action, bucket, key, result string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e := Entry{Seq: l.seq, Time: time.Now().UTC(), Actor: actor, Action: action, Bucket: bucket, Key: key, Result: result, Prev: l.last}
	e.Hash = e.digest()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	l.last = e.Hash
	return nil
}

// Close closes the file.
func (l *Log) Close() error { return l.f.Close() }

// Tail returns up to n most recent entries.
func (l *Log) Tail(n int) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries, err := read(l.path)
	if err != nil {
		return nil, err
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

// Verify checks the whole hash chain.
func Verify(path string) error {
	_, err := read(path)
	return err
}

func read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	prev := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%w: unreadable entry %d", ErrTampered, len(out)+1)
		}
		if e.Prev != prev || e.digest() != e.Hash {
			return nil, fmt.Errorf("%w at entry %d", ErrTampered, e.Seq)
		}
		prev = e.Hash
		out = append(out, e)
	}
	return out, sc.Err()
}
