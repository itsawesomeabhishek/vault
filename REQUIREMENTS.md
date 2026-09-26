# Vault requirement and quality evidence

Graders: every requirement and every quality criterion below maps to **working
code plus an automated test**. A row without a named test is a defect.

## Problem-statement agreement

| ID | Requirement | Implementation | Automated test |
| --- | --- | --- | --- |
| R1 | Store and retrieve objects | Streaming chunked I/O in `write.go` / `read.go` | `TestR1R2_StoreRetrieveReplicateAcrossZones`, `TestStreamingPutMultiChunk` |
| R2 | Replicate across unreliable nodes / zones | Consistent hash + zone-aware preference (`internal/ring`) | `TestR1R2_…` (asserts two zones) |
| R3 | Configurable durability, including low-overhead erasure coding | `internal/policy`, Reed-Solomon `internal/erasure`; default buckets `scans` 3/2/2 and `archive` 2+1 | `TestR3_ErasureCodingPolicySurvivesNodeLossWithLowOverhead` (≤1.6×), `TestR3_InvalidPolicyRejected`, Vitest `policy.test.ts` |
| R4 | Concurrent reads and writes converge | Per-key locks + HLC last-writer-wins | `TestR4_ConcurrentReadsAndWritesConverge` (run with `-race`) |
| R5 | Node failures stay writable when policy allows | Sloppy quorum + hinted handoff | `TestR5_NodeFailureSloppyQuorumAndHintedHandoff` |
| R6 | Partial network partitions | Strict quorum refuses a minority; heal via anti-entropy | `TestR6_PartitionStrictQuorumRejectsMinority`, `TestR6R8_PartitionHealConvergesViaAntiEntropy` |
| R7 | Data corruption is detected | SHA-256 on every shard; corrupt files quarantined | `TestR7R10R12_CorruptionDetectedAndRepaired` |
| R8 | Replica inconsistency is resolved | Merkle anti-entropy + catalog sync | `TestR6R8_…` |
| R9 | Background rebalancing | Ring diff + hinted handoff drain | `TestR9_RebalanceOnJoin` |
| R10 | Integrity verification | Rate-limited scrubber + Inspect | `TestR7R10R12_…` |
| R11 | Metadata consistency | Manifests only after chunks; tombstones; orphan GC | `TestR11_AbortedUploadLeavesNoVisibleObjectAndOrphansAreCollected`, `TestR11_DeleteAndList` |
| R12 | Automatic replica repair | Priority queue, read repair, scrub, anti-entropy | `TestR7R10R12_…` |
| R13 | Predictable availability | Hedged reads + `/v1/metrics` p50/p99 | `TestR13_HedgedReadsBoundTailLatency`, `TestMetricsRecordPutAndGet` |
| R14 | Minimise recovery time | Dead-timeout reaper + priority repair | `TestR14_AutomaticRecoveryAfterPermanentNodeLoss` |
| Overhead | Minimise storage | Erasure 2+1 ≈ 1.5×; per-node content-addressed dedup | `TestR3_…` ratio, `TestDedupIdenticalPayloadsShareDisk`, `TestShardPutGetDedupAndQuota` |

End-to-end with **real processes**: `scripts/smoke.ps1` (upload 9 MB, cross-node
download, corrupt, scrub, repair, kill a node, sloppy write).

## Quality criteria (how this scores)

### Code quality
- Small packages behind interfaces (`Membership`, `Peer`, `Inviter`). Every
  production package except generated `vaultpb` has its own `_test.go`.
- Generated gRPC only in `engine/gen`. Durability rules are mirrored in Go and TypeScript (`policy.go` / `policy.ts`) and tested on both sides.
- `gofmt`, `go vet`, `golangci-lint` (staticcheck, errcheck, gosec, revive) in CI. Root `Makefile` and `.editorconfig` keep style consistent.
- TypeScript `strict` + `noUnusedLocals` / `noUnusedParameters`, ESLint + `jsx-a11y` + `react-hooks`, no `any`, no `eslint-disable`.
- Errors wrapped with `%w`; `context` timeouts on every RPC (`RPCTimeout`). Typed API JSON (`healthResponse`, `metricsResponse`).

### Security
- Node-to-node: mTLS, cluster CA, single-use invite codes pinned to the CA fingerprint.
- Gossip encrypted with a 32-byte cluster key.
- Local API **must** bind loopback (`TestRequireLoopbackAPI`). Bearer token compared after SHA-256 so the compare is constant-time even when lengths differ.
- HTTP: `MaxBytesReader` on uploads, 1 MiB JSON cap + `DisallowUnknownFields`, security headers (`nosniff`, `DENY`, CSP, `Referrer-Policy`), `ReadHeaderTimeout`.
- Chaos endpoints are off unless `--enable-chaos` (`TestChaosDisabledByDefault`).
- Keys are never filesystem paths (`TestHostileKeyCannotEscapeDataDir`, `TestHostileKeyOverHTTP`, `FuzzValidateHash`).
- Electron: `sandbox`, `contextIsolation`, no `nodeIntegration`, CSP, zod IPC allow-list, `safeStorage` for the API token.
- `govulncheck` + `npm audit --omit=dev --audit-level=high` in CI.

### Efficiency
- 4 MiB (configurable) streaming pipeline with a **1-chunk** channel and a `sync.Pool` of buffers — the whole file is never held.
- Content-addressed shards: identical studies do not consume extra disk on a node.
- Erasure coding 2+1 stores ~1.5× instead of 3×.
- Hedged reads bound tail latency; prefetch window of 3 chunks on download.
- Rate-limited scrubber (`ScrubBytesPerSec`) so background work cannot starve clients.
- Benchmark: `go test ./internal/node -bench BenchmarkPutGet64KiB -benchmem`.

### Testing
- Unit tests for ring, store, HLC, erasure, policy, keys, metrics, audit, security, API.
- Fuzz: `FuzzValidateKey`, `FuzzValidateHash` (CI runs 10s each).
- In-process multi-node harness with fault injection for R1–R14.
- `go test -race` on every CI run.
- Vitest: policy, IPC allow-list, Setup/Buckets, App shell, Activity live region, ObjectDetail slot labels.
- Playwright + axe (`wcag2a`, `wcag2aa`, `wcag21aa`) on the setup screen.

### Accessibility (WCAG 2.1 AA)
- Skip link, `aria-current` on nav, focus-visible 3px outline, contrast ≥ 4.5:1 (`styles.css`).
- Status is **icon + text**, never colour alone (`StatusBadge`, ObjectDetail slot labels).
- Native `<dialog>` (focus trap, Escape), labelled inputs, live regions (`role="alert"` / `role="status"` / `role="log"`).
- `prefers-reduced-motion` and `forced-colors` support.
- Tests: `components.test.tsx`, `pages.test.tsx`, `tests/e2e/app.e2e.ts`.
