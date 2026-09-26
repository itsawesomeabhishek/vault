// Package api serves the node's local HTTP API used by the desktop app and
// scripts. It binds to loopback by default and requires a bearer token.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/audit"
	"github.com/hydra-software/vault/engine/internal/keys"
	"github.com/hydra-software/vault/engine/internal/node"
	"github.com/hydra-software/vault/engine/internal/policy"
)

// MetaHeaderPrefix carries user metadata on uploads and downloads.
const MetaHeaderPrefix = "X-Vault-Meta-"

// Inviter issues invite codes for new nodes.
type Inviter interface {
	Invite(ttl time.Duration) (code string, expires time.Time, err error)
}

// Server is the HTTP API.
type Server struct {
	Node    *node.Node
	Token   string
	Audit   *audit.Log
	Inviter Inviter
	Chaos   bool
	Log     *slog.Logger
}

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/metrics", s.metrics)
	mux.HandleFunc("GET /v1/buckets", s.listBuckets)
	mux.HandleFunc("PUT /v1/buckets/{bucket}", s.createBucket)
	mux.HandleFunc("DELETE /v1/buckets/{bucket}", s.deleteBucket)
	mux.HandleFunc("GET /v1/buckets/{bucket}/objects", s.listObjects)
	mux.HandleFunc("PUT /v1/buckets/{bucket}/objects/{key...}", s.putObject)
	mux.HandleFunc("GET /v1/buckets/{bucket}/objects/{key...}", s.getObject)
	mux.HandleFunc("HEAD /v1/buckets/{bucket}/objects/{key...}", s.headObject)
	mux.HandleFunc("DELETE /v1/buckets/{bucket}/objects/{key...}", s.deleteObject)
	mux.HandleFunc("GET /v1/buckets/{bucket}/inspect/{key...}", s.inspect)
	mux.HandleFunc("POST /v1/buckets/{bucket}/repair/{key...}", s.repair)
	mux.HandleFunc("POST /v1/maintenance/scrub", s.scrub)
	mux.HandleFunc("POST /v1/maintenance/anti-entropy", s.antiEntropy)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("GET /v1/audit", s.auditTail)
	mux.HandleFunc("POST /v1/invites", s.invite)
	mux.HandleFunc("POST /v1/chaos/corrupt", s.chaosCorrupt)
	mux.HandleFunc("POST /v1/chaos/drop", s.chaosDrop)
	mux.HandleFunc("POST /v1/chaos/partition", s.chaosPartition)
	mux.HandleFunc("POST /v1/chaos/heal", s.chaosHeal)
	return s.secure(mux)
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'none'")
		if r.URL.Path != "/v1/health" && !s.authorized(r) {
			writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || s.Token == "" {
		return false
	}
	// Hash first so the compare is always 32 bytes (ConstantTimeCompare is
	// not constant-time when the lengths differ).
	sumGot := sha256.Sum256([]byte(got))
	sumWant := sha256.Sum256([]byte(s.Token))
	return subtle.ConstantTimeCompare(sumGot[:], sumWant[:]) == 1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, node.ErrNotFound), errors.Is(err, node.ErrNoBucket):
		code = http.StatusNotFound
	case errors.Is(err, node.ErrBucketExists), errors.Is(err, node.ErrBucketNotEmpty):
		code = http.StatusConflict
	case errors.Is(err, keys.ErrInvalid), errors.Is(err, policy.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, node.ErrTooLarge):
		code = http.StatusRequestEntityTooLarge
	case errors.Is(err, node.ErrUnavailable):
		code = http.StatusServiceUnavailable
	default:
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			code = http.StatusRequestEntityTooLarge
		}
	}
	if code == http.StatusInternalServerError && s.Log != nil {
		s.Log.Error("request failed", "err", err)
	}
	writeErr(w, code, err.Error())
}

func (s *Server) record(r *http.Request, action, bucket, key string, err error) {
	if s.Audit == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	actor := r.Header.Get("X-Vault-Actor")
	if actor == "" || len(actor) > 64 {
		actor = "api"
	}
	_ = s.Audit.Record(actor, action, bucket, key, result)
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: request body: %v", keys.ErrInvalid, err)
	}
	return nil
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": s.Node.ID()})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Node.Status())
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	counters, latencies := s.Node.Metrics.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"counters": counters, "latencies": latencies})
}

type policyJSON struct {
	N      uint32 `json:"n"`
	K      uint32 `json:"k"`
	W      uint32 `json:"w"`
	R      uint32 `json:"r"`
	Sloppy bool   `json:"sloppy"`
}

type bucketJSON struct {
	Name           string     `json:"name"`
	Policy         policyJSON `json:"policy"`
	Description    string     `json:"description"`
	Overhead       float64    `json:"overhead"`
	FaultTolerance uint32     `json:"faultTolerance"`
	Created        time.Time  `json:"created"`
}

func toBucketJSON(b *vaultpb.BucketConfig) bucketJSON {
	p := b.Policy
	return bucketJSON{
		Name: b.Name, Policy: policyJSON{N: p.N, K: p.K, W: p.W, R: p.R, Sloppy: p.Sloppy},
		Description: policy.Describe(p), Overhead: policy.Overhead(p), FaultTolerance: policy.FaultTolerance(p),
		Created: time.Unix(0, b.CreatedUnixNano).UTC(),
	}
}

func (s *Server) listBuckets(w http.ResponseWriter, _ *http.Request) {
	out := []bucketJSON{}
	for _, b := range s.Node.Buckets() {
		out = append(out, toBucketJSON(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createBucket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Policy policyJSON `json:"policy"`
	}
	bucket := r.PathValue("bucket")
	if err := decodeJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	p := &vaultpb.Policy{N: req.Policy.N, K: req.Policy.K, W: req.Policy.W, R: req.Policy.R, Sloppy: req.Policy.Sloppy}
	cfg, err := s.Node.CreateBucket(r.Context(), bucket, p)
	s.record(r, "bucket.create", bucket, "", err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBucketJSON(cfg))
}

func (s *Server) deleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	err := s.Node.DeleteBucket(r.Context(), bucket)
	s.record(r, "bucket.delete", bucket, "", err)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	bucket := r.PathValue("bucket")
	items, next, err := s.Node.List(r.Context(), bucket, q.Get("prefix"), q.Get("startAfter"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	if patient := strings.TrimSpace(q.Get("patient")); patient != "" {
		filtered := items[:0]
		for _, it := range items {
			if strings.EqualFold(it.Metadata["patient-id"], patient) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

func metadataFrom(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if name, ok := strings.CutPrefix(k, MetaHeaderPrefix); ok && len(v) > 0 {
			out[strings.ToLower(name)] = v[0]
		}
	}
	return out
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.Node.MaxObjectBytes())
	m, err := s.Node.Put(r.Context(), bucket, key, r.Body, ct, metadataFrom(r.Header))
	s.record(r, "object.put", bucket, key, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("ETag", `"`+m.Etag+`"`)
	writeJSON(w, http.StatusCreated, node.Summarize(m))
}

func setObjectHeaders(w http.ResponseWriter, m *vaultpb.Manifest) {
	h := w.Header()
	h.Set("Content-Type", m.ContentType)
	h.Set("Content-Length", strconv.FormatUint(m.Size, 10))
	h.Set("ETag", `"`+m.Etag+`"`)
	h.Set("Last-Modified", time.Unix(0, m.GetVersion().GetWall()).UTC().Format(http.TimeFormat))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	for k, v := range m.Metadata {
		h.Set(MetaHeaderPrefix+k, v)
	}
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	m, body, err := s.Node.Get(r.Context(), bucket, key)
	s.record(r, "object.get", bucket, key, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer body.Close()
	setObjectHeaders(w, m)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil && s.Log != nil {
		s.Log.Warn("download aborted", "bucket", bucket, "key", key, "err", err)
	}
}

func (s *Server) headObject(w http.ResponseWriter, r *http.Request) {
	m, err := s.Node.Head(r.Context(), r.PathValue("bucket"), r.PathValue("key"))
	if err != nil {
		s.fail(w, err)
		return
	}
	setObjectHeaders(w, m)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	err := s.Node.Delete(r.Context(), bucket, key)
	s.record(r, "object.delete", bucket, key, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	ins, err := s.Node.Inspect(r.Context(), r.PathValue("bucket"), r.PathValue("key"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ins)
}

func (s *Server) repair(w http.ResponseWriter, r *http.Request) {
	s.Node.RepairNow(r.PathValue("bucket"), r.PathValue("key"))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "repair scheduled"})
}

func (s *Server) scrub(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Node.Scrub(ctx))
}

func (s *Server) antiEntropy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"scheduled": s.Node.AntiEntropy(r.Context())})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := s.Node.Events.Subscribe()
	defer cancel()
	history, _ := strconv.Atoi(r.URL.Query().Get("history"))
	for _, e := range s.Node.Events.Recent(min(max(history, 0), 200)) {
		writeSSE(w, e)
	}
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			writeSSE(w, e)
			fl.Flush()
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func writeSSE(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func (s *Server) auditTail(w http.ResponseWriter, r *http.Request) {
	if s.Audit == nil {
		writeJSON(w, http.StatusOK, []audit.Entry{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.Audit.Tail(min(max(limit, 1), 500))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	if s.Inviter == nil {
		writeErr(w, http.StatusNotImplemented, "invites unavailable")
		return
	}
	code, exp, err := s.Inviter.Invite(30 * time.Minute)
	s.record(r, "cluster.invite", "", "", err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "expires": exp})
}

type objectRef struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

func (s *Server) chaosAllowed(w http.ResponseWriter) bool {
	if !s.Chaos {
		writeErr(w, http.StatusForbidden, "chaos testing is disabled on this node (start with --enable-chaos)")
		return false
	}
	return true
}

func (s *Server) chaosCorrupt(w http.ResponseWriter, r *http.Request) {
	if !s.chaosAllowed(w) {
		return
	}
	var ref objectRef
	if err := decodeJSON(r, &ref); err != nil {
		s.fail(w, err)
		return
	}
	shard, err := s.Node.CorruptLocal(ref.Bucket, ref.Key)
	s.record(r, "chaos.corrupt", ref.Bucket, ref.Key, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"corruptedShard": shard})
}

func (s *Server) chaosDrop(w http.ResponseWriter, r *http.Request) {
	if !s.chaosAllowed(w) {
		return
	}
	var ref objectRef
	if err := decodeJSON(r, &ref); err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.Node.DropLocal(ref.Bucket, ref.Key)
	s.record(r, "chaos.drop", ref.Bucket, ref.Key, err)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"droppedShards": n})
}

func (s *Server) chaosPartition(w http.ResponseWriter, r *http.Request) {
	if !s.chaosAllowed(w) {
		return
	}
	var req struct {
		Peer    string `json:"peer"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if req.Peer == "" || req.Peer == s.Node.ID() {
		writeErr(w, http.StatusBadRequest, "peer must be another node's ID")
		return
	}
	if req.Enabled {
		s.Node.Faults().Block(s.Node.ID(), req.Peer)
	} else {
		s.Node.Faults().Unblock(s.Node.ID(), req.Peer)
	}
	s.record(r, "chaos.partition", "", req.Peer, nil)
	s.Node.Events.Publish("chaos.partition", "warning", "", "", fmt.Sprintf("link to %s %s", req.Peer, map[bool]string{true: "cut", false: "restored"}[req.Enabled]))
	writeJSON(w, http.StatusOK, map[string]any{"partitionedFrom": s.Node.Faults().Blocked(s.Node.ID())})
}

func (s *Server) chaosHeal(w http.ResponseWriter, r *http.Request) {
	if !s.chaosAllowed(w) {
		return
	}
	s.Node.Faults().Heal()
	s.record(r, "chaos.heal", "", "", nil)
	s.Node.Events.Publish("chaos.heal", "info", "", "", "all injected faults removed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "healed"})
}
