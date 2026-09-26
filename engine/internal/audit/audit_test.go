package audit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChainDetectsTampering(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := l.Record("dashboard", "get", "scans", k, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	if err := Verify(p); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Record("x", "put", "scans", "d", "ok"); err != nil {
		t.Fatal(err)
	}
	tail, _ := l2.Tail(2)
	if len(tail) != 2 || tail[1].Seq != 4 {
		t.Fatalf("tail = %+v", tail)
	}
	l2.Close()

	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, bytes.Replace(raw, []byte(`"key":"b"`), []byte(`"key":"z"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(p); !errors.Is(err, ErrTampered) {
		t.Fatalf("expected tamper detection, got %v", err)
	}
}
