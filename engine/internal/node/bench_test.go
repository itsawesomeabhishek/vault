package node

import (
	"bytes"
	"testing"

	"github.com/hydra-software/vault/engine/internal/policy"
)

func BenchmarkPutGet64KiB(b *testing.B) {
	c := newTestCluster(b, 3, nil)
	c.createBucket("scans", policy.Replicated(3, 2, 2, true))
	data := bytes.Repeat([]byte{7}, 64<<10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mustPut(b, c.node("n1"), "scans", "bench.dcm", data)
		got := mustGet(b, c.node("n2"), "scans", "bench.dcm")
		if !bytes.Equal(got, data) {
			b.Fatal("mismatch")
		}
	}
}
