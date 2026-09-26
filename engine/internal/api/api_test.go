package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hydra-software/vault/engine/internal/audit"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/node"
	"github.com/hydra-software/vault/engine/internal/transport"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	members := membership.NewStatic()
	members.Upsert(membership.Member{ID: "n1", Zone: "a"})
	netw := transport.NewLocalNetwork()
	n, err := node.New(node.Config{ID: "n1", DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, members.View("n1"), netw.Dialer("n1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	netw.Register("n1", n.Server())
	n.Start()
	t.Cleanup(func() { _ = n.Close() })
	al, err := audit.Open(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = al.Close() })
	srv := httptest.NewServer((&Server{Node: n, Token: "secret", Audit: al}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, token, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestAuthRequired(t *testing.T) {
	srv := newServer(t)
	if r, _ := do(t, "GET", srv.URL+"/v1/status", "", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token = %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", srv.URL+"/v1/status", "wrong", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status with bad token = %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", srv.URL+"/v1/health", "", "", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", r.StatusCode)
	}
}

func TestObjectLifecycleOverHTTP(t *testing.T) {
	srv := newServer(t)
	tok := "secret"
	if r, b := do(t, "PUT", srv.URL+"/v1/buckets/scans", tok, `{"policy":{"n":1,"k":1,"w":1,"r":1}}`, nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("create bucket = %d %s", r.StatusCode, b)
	}
	if r, _ := do(t, "PUT", srv.URL+"/v1/buckets/scans", tok, `{"policy":{"n":3,"k":1,"w":1,"r":1}}`, nil); r.StatusCode != http.StatusBadRequest && r.StatusCode != http.StatusConflict {
		t.Fatalf("invalid/duplicate bucket = %d", r.StatusCode)
	}
	hdr := map[string]string{"Content-Type": "application/dicom", "X-Vault-Meta-Patient-Id": "P-7"}
	if r, b := do(t, "PUT", srv.URL+"/v1/buckets/scans/objects/p/7/ct.dcm", tok, "DICOM-BYTES", hdr); r.StatusCode != http.StatusCreated {
		t.Fatalf("put = %d %s", r.StatusCode, b)
	}
	r, b := do(t, "GET", srv.URL+"/v1/buckets/scans/objects/p/7/ct.dcm", tok, "", nil)
	if r.StatusCode != http.StatusOK || b != "DICOM-BYTES" || r.Header.Get("X-Vault-Meta-Patient-Id") != "P-7" {
		t.Fatalf("get = %d %q %v", r.StatusCode, b, r.Header)
	}
	if _, b := do(t, "GET", srv.URL+"/v1/buckets/scans/objects?patient=p-7", tok, "", nil); !strings.Contains(b, "ct.dcm") {
		t.Fatalf("patient search = %s", b)
	}
	if r, b := do(t, "GET", srv.URL+"/v1/buckets/scans/inspect/p/7/ct.dcm", tok, "", nil); r.StatusCode != http.StatusOK || !strings.Contains(b, `"healthySlots":1`) {
		t.Fatalf("inspect = %d %s", r.StatusCode, b)
	}
	if r, _ := do(t, "DELETE", srv.URL+"/v1/buckets/scans/objects/p/7/ct.dcm", tok, "", nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", srv.URL+"/v1/buckets/scans/objects/p/7/ct.dcm", tok, "", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", r.StatusCode)
	}
	if _, b := do(t, "GET", srv.URL+"/v1/audit?limit=10", tok, "", nil); !strings.Contains(b, "object.delete") {
		t.Fatalf("audit = %s", b)
	}
}

func TestChaosDisabledByDefault(t *testing.T) {
	srv := newServer(t)
	if r, _ := do(t, "POST", srv.URL+"/v1/chaos/heal", "secret", "", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("chaos without flag = %d", r.StatusCode)
	}
}

func TestRejectsUnknownFields(t *testing.T) {
	srv := newServer(t)
	if r, _ := do(t, "PUT", srv.URL+"/v1/buckets/abc", "secret", `{"policy":{"n":1,"k":1,"w":1,"r":1},"evil":1}`, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", r.StatusCode)
	}
}

func TestMetricsAndSecurityHeaders(t *testing.T) {
	srv := newServer(t)
	if r, _ := do(t, "GET", srv.URL+"/v1/metrics", "wrong-length", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token length = %d", r.StatusCode)
	}
	r, b := do(t, "GET", srv.URL+"/v1/metrics", "secret", "", nil)
	if r.StatusCode != http.StatusOK || !strings.Contains(b, `"counters"`) {
		t.Fatalf("metrics = %d %s", r.StatusCode, b)
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" || r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing security headers: %v", r.Header)
	}
}

func TestHostileKeyOverHTTP(t *testing.T) {
	srv := newServer(t)
	tok := "secret"
	if r, b := do(t, "PUT", srv.URL+"/v1/buckets/scans", tok, `{"policy":{"n":1,"k":1,"w":1,"r":1}}`, nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("bucket = %d %s", r.StatusCode, b)
	}
	key := "..%2F..%2Fwindows%2Fsystem32%2Fevil.dcm"
	if r, b := do(t, "PUT", srv.URL+"/v1/buckets/scans/objects/"+key, tok, "bytes", nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("put hostile key = %d %s", r.StatusCode, b)
	}
	if r, b := do(t, "GET", srv.URL+"/v1/buckets/scans/objects/"+key, tok, "", nil); r.StatusCode != http.StatusOK || b != "bytes" {
		t.Fatalf("get hostile key = %d %q", r.StatusCode, b)
	}
}
