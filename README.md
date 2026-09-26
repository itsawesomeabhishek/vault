# Vault

Fault-tolerant distributed object storage for medical imaging archives. Each
computer runs two storage nodes. Scans are chunked, checksummed, encrypted, and
written to several nodes before the upload is acknowledged. Damaged or missing
copies are repaired automatically.

This is not a mock. File bytes live on disk under each node's data directory.
Nodes find each other with gossip, talk over gRPC with mutual TLS, and agree on
ownership with a consistent-hash ring.

## Install on Windows

1. Run `app/dist/Vault-Setup.exe` (built by `scripts/build.ps1`).
2. Allow the installer to add Windows Firewall rules for TCP `19000-19001` and
   TCP/UDP `17946-17947` (the two local nodes). The HTTP API stays on localhost.
3. Open **Vault** from the Start menu.

## Two computers (the demo)

Both laptops must reach each other. Same Wi-Fi works. Different networks: install
[Tailscale](https://tailscale.com) on both and leave the advertise address blank
— Vault prefers a `100.x` Tailscale address.

**Computer A (first)**

1. Choose **Create a new cluster**.
2. Name this computer (the failure zone), for example `laptop-a`.
3. After it starts, open **Cluster → Create invite** and copy the code.

**Computer B (friend)**

1. Install the same `Vault-Setup.exe`.
2. Choose **Join an existing cluster** and paste the invite.
3. Name this computer something else, for example `laptop-b`.

Each invite is single-use and pins the cluster CA fingerprint, so B cannot be
tricked onto a different cluster.

## Demo script (Failure lab)

Use a small scan (a few MB) in the `scans` bucket.

1. **Upload** on A. Open the scan. You should see 3 replica slots, two zones.
2. **Corrupt a local copy** on A. Integrity goes red. Click **Repair now** (or
   wait for the scrubber). The slot returns to healthy; download still matches.
3. **Stop a local node** on A. Upload another file — sloppy quorum writes a hint
   to a neighbour. Start the node again; the hint is handed off.
4. **Partition** A from B, then **Heal**. Anti-entropy converges both sides.
5. Download the original file from B. Bytes must match.

The `archive` bucket uses erasure coding 2+1 (1.5× storage instead of 3×) and
still survives one node loss.

## Develop

```powershell
# Engine
cd engine
go test ./...
..\scripts\smoke.ps1

# Desktop
cd ..\app
npm install
npm run typecheck
npm test
npm run lint
npm run dev          # live UI (uses app/resources/bin/vault-node.exe)
npm run dist         # Vault-Setup.exe
```

Or one shot: `.\scripts\build.ps1`.

## Layout

```
engine/     Go storage node (gRPC + gossip + HTTP API)
app/        Electron + React console
scripts/    proto codegen, smoke test, Windows build
```

See [REQUIREMENTS.md](REQUIREMENTS.md) for the mapping from each problem-statement
requirement to its code and test.
