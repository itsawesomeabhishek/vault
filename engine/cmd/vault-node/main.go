// Command vault-node runs one Vault storage node.
//
// First node:   vault-node --data-dir D --init
// Other nodes:  vault-node --data-dir D --join <invite code>
// Restart:      vault-node --data-dir D
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/api"
	"github.com/hydra-software/vault/engine/internal/audit"
	"github.com/hydra-software/vault/engine/internal/events"
	"github.com/hydra-software/vault/engine/internal/membership"
	"github.com/hydra-software/vault/engine/internal/node"
	"github.com/hydra-software/vault/engine/internal/policy"
	"github.com/hydra-software/vault/engine/internal/security"
	"github.com/hydra-software/vault/engine/internal/transport"
)

type options struct {
	dataDir       string
	zone          string
	bind          string
	advertise     string
	rpcPort       int
	gossipPort    int
	apiAddr       string
	init          bool
	join          string
	chunkMiB      int
	maxGiB        float64
	encrypt       bool
	chaos         bool
	deadTimeout   time.Duration
	scrubInterval time.Duration
	aeInterval    time.Duration
	logJSON       bool
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.dataDir, "data-dir", "", "directory for this node's data (required)")
	flag.StringVar(&o.zone, "zone", "", "failure zone, e.g. the computer name (default: hostname)")
	flag.StringVar(&o.bind, "bind", "0.0.0.0", "IP to listen on for node-to-node traffic")
	flag.StringVar(&o.advertise, "advertise", "", "IP other nodes use to reach this node (default: auto-detect, Tailscale preferred)")
	flag.IntVar(&o.rpcPort, "rpc-port", 19000, "gRPC port (mutual TLS)")
	flag.IntVar(&o.gossipPort, "gossip-port", 17946, "gossip port (TCP+UDP, encrypted)")
	flag.StringVar(&o.apiAddr, "api-addr", "127.0.0.1:18080", "local HTTP API address")
	flag.BoolVar(&o.init, "init", false, "create a new cluster")
	flag.StringVar(&o.join, "join", "", "invite code of an existing cluster")
	flag.IntVar(&o.chunkMiB, "chunk-mib", 4, "chunk size in MiB")
	flag.Float64Var(&o.maxGiB, "max-gib", 0, "storage quota in GiB (0 = unlimited)")
	flag.BoolVar(&o.encrypt, "encrypt-at-rest", true, "encrypt shards with the cluster data key (AES-256-GCM)")
	flag.BoolVar(&o.chaos, "enable-chaos", false, "allow chaos-testing endpoints")
	flag.DurationVar(&o.deadTimeout, "dead-timeout", 10*time.Minute, "how long a failed node keeps its data ownership before re-replication")
	flag.DurationVar(&o.scrubInterval, "scrub-interval", 10*time.Minute, "interval between integrity scrubs")
	flag.DurationVar(&o.aeInterval, "anti-entropy-interval", 15*time.Second, "interval between Merkle anti-entropy rounds")
	flag.BoolVar(&o.logJSON, "log-json", false, "log in JSON")
	flag.Parse()
	return o
}

func main() {
	o := parseFlags()
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	if o.logJSON {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	log := slog.New(handler)
	if err := run(o, log); err != nil {
		log.Error("fatal", "err", err)
		emit(map[string]any{"event": "fatal", "error": err.Error()})
		os.Exit(1)
	}
}

// emit writes a machine-readable line to stdout for the desktop supervisor.
func emit(v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func run(o options, log *slog.Logger) error {
	if o.dataDir == "" {
		return errors.New("--data-dir is required")
	}
	token := os.Getenv("VAULT_API_TOKEN")
	if len(token) < 16 {
		return errors.New("VAULT_API_TOKEN must be set to a random string of at least 16 characters")
	}
	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		return err
	}
	if o.zone == "" {
		o.zone, _ = os.Hostname()
	}
	nodeID, err := loadNodeID(o.dataDir)
	if err != nil {
		return err
	}
	advertise := o.advertise
	if advertise == "" {
		advertise = detectIP()
	}
	rpcAddr := net.JoinHostPort(advertise, strconv.Itoa(o.rpcPort))
	clusterDir := filepath.Join(o.dataDir, "cluster")
	var seeds []string
	created := false

	switch _, err := security.Load(clusterDir); {
	case err == nil:
		seeds = loadPeers(o.dataDir)
	case errors.Is(err, security.ErrNoCluster) && o.init:
		secrets, caCert, caKey, err := security.NewClusterCA()
		if err != nil {
			return err
		}
		if err := security.Save(clusterDir, nodeID, secrets, caCert, caKey, []net.IP{net.ParseIP(advertise)}); err != nil {
			return err
		}
		created = true
		log.Info("created new cluster", "cluster", secrets.ClusterID)
	case errors.Is(err, security.ErrNoCluster) && o.join != "":
		if seeds, err = joinCluster(o.join, nodeID, clusterDir, advertise); err != nil {
			return err
		}
	case errors.Is(err, security.ErrNoCluster):
		return errors.New("this node is not in a cluster yet: start with --init or --join <invite code>")
	default:
		return err
	}
	mat, err := security.Load(clusterDir)
	if err != nil {
		return err
	}

	gossip, err := membership.NewGossip(membership.GossipConfig{
		Self:     membership.Member{ID: nodeID, Zone: o.zone, RPCAddr: rpcAddr, APIAddr: o.apiAddr},
		BindAddr: o.bind, BindPort: o.gossipPort, Advertise: advertise, SecretKey: mat.Secrets.GossipKey, DeadTimeout: o.deadTimeout,
	})
	if err != nil {
		return err
	}

	dialer := transport.NewGRPCDialer(mat.ClientTLS())
	defer dialer.Close()
	cfg := node.Config{
		ID: nodeID, Zone: o.zone, DataDir: o.dataDir, ChunkSize: o.chunkMiB << 20,
		MaxBytes: int64(o.maxGiB * float64(1<<30)), ScrubInterval: o.scrubInterval, AntiEntropyInterval: o.aeInterval, Logger: log,
	}
	if o.encrypt {
		cfg.EncryptionKey = mat.Secrets.DataKey
	}
	n, err := node.New(cfg, gossip, dialer, nil)
	if err != nil {
		return err
	}
	defer n.Close()

	invites := security.NewInvites()
	inviter := &inviter{invites: invites, mat: mat, rpcAddr: rpcAddr}
	join := func(_ context.Context, req *vaultpb.JoinRequest) (*vaultpb.JoinResponse, error) {
		if err := invites.Redeem(req.Token); err != nil {
			n.Events.Publish("cluster.join_rejected", events.Warning, "", "", "rejected a join attempt with an invalid invite")
			return nil, err
		}
		n.Events.Publish("cluster.joined", events.Info, "", "", "node "+req.NodeId+" joined with an invite")
		return &vaultpb.JoinResponse{
			ClusterId: mat.Secrets.ClusterID, CaCertPem: mat.CACertPE, CaKeyPem: mat.CAKeyPE,
			GossipKey: mat.Secrets.GossipKey, DataKey: mat.Secrets.DataKey, GossipSeeds: gossip.GossipAddrs(), Catalog: n.Catalog(),
		}, nil
	}
	lis, err := net.Listen("tcp", net.JoinHostPort(o.bind, strconv.Itoa(o.rpcPort)))
	if err != nil {
		return fmt.Errorf("listen rpc: %w", err)
	}
	grpcSrv := transport.NewGRPCServer(n.Server(), join, mat.ServerTLS())
	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			log.Error("grpc server stopped", "err", err)
		}
	}()
	defer grpcSrv.GracefulStop()

	if len(seeds) > 0 {
		if joined, err := gossip.Join(seeds); err != nil {
			log.Warn("could not reach any known peer yet; will keep running", "err", err)
		} else {
			log.Info("joined gossip", "peers", joined)
		}
	}
	n.Start()
	if created {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = n.EnsureBucket(ctx, "scans", policy.Replicated(3, 2, 2, true))
		_ = n.EnsureBucket(ctx, "archive", policy.Erasure(2, 1, 2, 2, true))
		cancel()
	}
	go persistPeers(o.dataDir, gossip)

	al, err := audit.Open(filepath.Join(o.dataDir, "audit.log"))
	if err != nil {
		return err
	}
	defer al.Close()
	if al.Tampered != "" {
		n.Events.Publish("audit.tampered", events.Error, "", "", "audit log chain was broken; evidence preserved at "+al.Tampered)
	}
	apiSrv := &http.Server{
		Addr:              o.apiAddr,
		Handler:           (&api.Server{Node: n, Token: token, Audit: al, Inviter: inviter, Chaos: o.chaos, Log: log}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	apiLis, err := net.Listen("tcp", o.apiAddr)
	if err != nil {
		return fmt.Errorf("listen api: %w", err)
	}
	go func() {
		if err := apiSrv.Serve(apiLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api server stopped", "err", err)
		}
	}()

	emit(map[string]any{"event": "ready", "id": nodeID, "zone": o.zone, "rpc": rpcAddr, "api": o.apiAddr, "cluster": mat.Secrets.ClusterID})
	log.Info("vault node ready", "id", nodeID, "rpc", rpcAddr, "api", o.apiAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(ctx)
	return nil
}

type inviter struct {
	invites *security.Invites
	mat     *security.Material
	rpcAddr string
}

func (i *inviter) Invite(ttl time.Duration) (string, time.Time, error) {
	tok, exp, err := i.invites.Issue(ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	code, err := security.Invite{
		ClusterID: i.mat.Secrets.ClusterID, Addr: i.rpcAddr, Token: tok,
		CAFingerprint: security.Fingerprint(i.mat.CACert), ExpiresUnix: exp.Unix(),
	}.Encode()
	return code, exp, err
}

func joinCluster(code, nodeID, clusterDir, advertise string) ([]string, error) {
	inv, err := security.DecodeInvite(code)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := transport.JoinCluster(ctx, inv.Addr, security.PinnedTLS(inv.CAFingerprint), &vaultpb.JoinRequest{Token: inv.Token, NodeId: nodeID})
	if err != nil {
		return nil, fmt.Errorf("could not join via %s: %w", inv.Addr, err)
	}
	secrets := security.Secrets{ClusterID: resp.ClusterId, GossipKey: resp.GossipKey, DataKey: resp.DataKey}
	if err := security.Save(clusterDir, nodeID, secrets, resp.CaCertPem, resp.CaKeyPem, []net.IP{net.ParseIP(advertise)}); err != nil {
		return nil, err
	}
	savePeers(filepath.Dir(clusterDir), resp.GossipSeeds)
	return resp.GossipSeeds, nil
}

func loadNodeID(dir string) (string, error) {
	p := filepath.Join(dir, "node-id")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	suffix, err := security.RandomHex(4)
	if err != nil {
		return "", err
	}
	id := "node-" + suffix
	return id, os.WriteFile(p, []byte(id), 0o600)
}

func loadPeers(dir string) []string {
	var peers []string
	b, err := os.ReadFile(filepath.Join(dir, "peers.json"))
	if err == nil {
		_ = json.Unmarshal(b, &peers)
	}
	return peers
}

func savePeers(dir string, peers []string) {
	if len(peers) == 0 {
		return
	}
	b, _ := json.Marshal(peers)
	_ = os.WriteFile(filepath.Join(dir, "peers.json"), b, 0o600)
}

func persistPeers(dir string, g *membership.Gossip) {
	ch, cancel := g.Subscribe()
	defer cancel()
	for range ch {
		self := g.Self().GossipAddr
		var peers []string
		for _, a := range g.GossipAddrs() {
			if a != self {
				peers = append(peers, a)
			}
		}
		savePeers(dir, peers)
	}
}

// detectIP prefers a Tailscale address (100.64.0.0/10, reachable across
// networks), then a private LAN address, then loopback.
func detectIP() string {
	_, tailnet, _ := net.ParseCIDR("100.64.0.0/10")
	var lan string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			if tailnet.Contains(ipn.IP) {
				return ipn.IP.String()
			}
			if lan == "" && ipn.IP.IsPrivate() {
				lan = ipn.IP.String()
			}
		}
	}
	if lan != "" {
		return lan
	}
	return "127.0.0.1"
}
