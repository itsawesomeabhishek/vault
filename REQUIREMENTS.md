# Vault requirement traceability

Every requirement from the problem statement is implemented in the Go engine and
exercised by a named test. The Electron app is the operator console; it does not
replace the storage engine.

| ID | Requirement | Implementation | Test |
| --- | --- | --- | --- |
| R1 | Store and retrieve objects | `engine/internal/node/write.go`, `read.go`; 4 MiB content-addressed chunks | `TestR1R2_StoreRetrieveReplicateAcrossZones` |
| R2 | Replicate across unreliable nodes / zones | Consistent hash ring + zone-aware preference list (`internal/ring`) | same + zone assertion |
| R3 | Configurable replication and durability, including low-overhead erasure coding | `internal/policy`, Reed-Solomon `internal/erasure`; default buckets `scans` (3/2/2) and `archive` (2+1) | `TestR3_ErasureCodingPolicySurvivesNodeLossWithLowOverhead`, `TestR3_InvalidPolicyRejected` |
| R4 | Concurrent reads and writes converge | HLC last-writer-wins manifests; pipelined chunk writes | `TestR4_ConcurrentReadsAndWritesConverge` |
| R5 | Node failures stay writable when policy allows | Sloppy quorum + hinted handoff (`write.go`, `repair.go`) | `TestR5_NodeFailureSloppyQuorumAndHintedHandoff` |
| R6 | Partial network partitions | Strict quorum refuses a minority; partition heals via anti-entropy | `TestR6_PartitionStrictQuorumRejectsMinority`, `TestR6R8_PartitionHealConvergesViaAntiEntropy` |
| R7 | Data corruption is detected | SHA-256 on every shard; corrupt files are quarantined | `TestR7R10R12_CorruptionDetectedAndRepaired` |
| R8 | Replica inconsistency is resolved | Merkle anti-entropy + catalog sync | `TestR6R8_PartitionHealConvergesViaAntiEntropy` |
| R9 | Background rebalancing when membership changes | Ring diff + hinted handoff drain (`Rebalance`) | `TestR9_RebalanceOnJoin` |
| R10 | Integrity verification | Background scrubber walks owned shards | `TestR7R10R12_CorruptionDetectedAndRepaired` |
| R11 | Metadata consistency | Manifests written only after chunks; tombstones; orphan GC | `TestR11_AbortedUploadLeavesNoVisibleObjectAndOrphansAreCollected`, `TestR11_DeleteAndList` |
| R12 | Automatic replica repair | Priority repair queue, read repair, scrub, anti-entropy | `TestR7R10R12_CorruptionDetectedAndRepaired` |
| R13 | Predictable availability / bound tail latency | Hedged reads after a short delay | `TestR13_HedgedReadsBoundTailLatency` |
| R14 | Minimise recovery time after permanent loss | Dead-timeout reaper + repair + rebalance | `TestR14_AutomaticRecoveryAfterPermanentNodeLoss` |

End-to-end, with real processes (not the in-process harness): `scripts/smoke.ps1`
starts four `vault-node` processes, uploads a 9 MB file, downloads it from another
node, corrupts a shard, scrubs, repairs, kills a node, and performs a sloppy write.

## Quality criteria

| Criterion | How it is satisfied |
| --- | --- |
| Code quality | Small packages, generated gRPC only in `engine/gen`, shared policy rules mirrored in TypeScript |
| Security | mTLS with a cluster CA, encrypted gossip, AES-256-GCM at rest, invite codes pinned to the CA fingerprint, Electron `sandbox` + `contextIsolation` + CSP, zod-validated IPC, `safeStorage` for the API token, constant-time bearer compare |
| Efficiency | 4 MiB chunk pipeline, content-addressed dedup, erasure coding for archive, hedged reads, prefetch window of 3 chunks |
| Testing | `go test ./...`, requirement tests R1–R14, API tests, `scripts/smoke.ps1`, Vitest (policy + a11y components), Playwright + axe |
| Accessibility | WCAG 2.1 AA: skip link, `aria-current`, status as icon+text, live regions, native `<dialog>`, labelled inputs, 4.5:1 contrast, `prefers-reduced-motion` |
| Problem-statement agreement | This table. A requirement without a named test is a defect. |
