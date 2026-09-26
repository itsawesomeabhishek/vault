package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/hydra-software/vault/engine/gen/vaultpb"
	"github.com/hydra-software/vault/engine/internal/membership"
)

// MaxMessageBytes bounds gRPC messages; shards are at most one chunk.
const MaxMessageBytes = 32 << 20

// JoinFunc handles an unauthenticated join request carrying an invite token.
type JoinFunc func(ctx context.Context, req *vaultpb.JoinRequest) (*vaultpb.JoinResponse, error)

// NewGRPCServer exposes a Peer over gRPC. Every method except Join requires a
// client certificate verified against the cluster CA; the certificate's
// common name becomes the caller identity.
func NewGRPCServer(p Peer, join JoinFunc, tlsCfg *tls.Config) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxMessageBytes),
		grpc.MaxSendMsgSize(MaxMessageBytes),
		grpc.UnaryInterceptor(authInterceptor),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
		grpc.ConnectionTimeout(10 * time.Second),
	}
	if tlsCfg != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	s := grpc.NewServer(opts...)
	vaultpb.RegisterNodeServer(s, &grpcServer{p: p, join: join})
	return s
}

func authInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if info.FullMethod == vaultpb.Node_Join_FullMethodName {
		return h(ctx, req)
	}
	pr, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no peer")
	}
	ti, ok := pr.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return nil, status.Error(codes.Unauthenticated, "client certificate required")
	}
	return h(WithCaller(ctx, ti.State.VerifiedChains[0][0].Subject.CommonName), req)
}

func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrCorrupt):
		return status.Error(codes.DataLoss, err.Error())
	case errors.Is(err, ErrUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func fromStatus(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, st.Message())
	case codes.DataLoss:
		return fmt.Errorf("%w: %s", ErrCorrupt, st.Message())
	case codes.Internal, codes.InvalidArgument, codes.PermissionDenied, codes.ResourceExhausted:
		return errors.New(st.Message())
	default:
		return fmt.Errorf("%w: %s", ErrUnavailable, st.Message())
	}
}

type grpcServer struct {
	vaultpb.UnimplementedNodeServer
	p    Peer
	join JoinFunc
}

func (s *grpcServer) Ping(ctx context.Context, _ *vaultpb.PingRequest) (*vaultpb.PingResponse, error) {
	id, err := s.p.Ping(ctx)
	return &vaultpb.PingResponse{NodeId: id}, toStatus(err)
}

func (s *grpcServer) PutShard(ctx context.Context, r *vaultpb.PutShardRequest) (*vaultpb.PutShardResponse, error) {
	return &vaultpb.PutShardResponse{}, toStatus(s.p.PutShard(ctx, r.Hash, r.Data))
}

func (s *grpcServer) GetShard(ctx context.Context, r *vaultpb.GetShardRequest) (*vaultpb.GetShardResponse, error) {
	data, err := s.p.GetShard(ctx, r.Hash)
	if err != nil {
		return nil, toStatus(err)
	}
	return &vaultpb.GetShardResponse{Data: data}, nil
}

func (s *grpcServer) HasShards(ctx context.Context, r *vaultpb.HasShardsRequest) (*vaultpb.HasShardsResponse, error) {
	present, err := s.p.HasShards(ctx, r.Hashes, r.Verify)
	return &vaultpb.HasShardsResponse{Present: present}, toStatus(err)
}

func (s *grpcServer) PutManifest(ctx context.Context, r *vaultpb.PutManifestRequest) (*vaultpb.PutManifestResponse, error) {
	applied, err := s.p.PutManifest(ctx, r.Manifest, r.HintFor)
	return &vaultpb.PutManifestResponse{Applied: applied}, toStatus(err)
}

func (s *grpcServer) GetManifest(ctx context.Context, r *vaultpb.GetManifestRequest) (*vaultpb.GetManifestResponse, error) {
	m, err := s.p.GetManifest(ctx, r.Bucket, r.Key)
	return &vaultpb.GetManifestResponse{Manifest: m}, toStatus(err)
}

func (s *grpcServer) ListManifests(ctx context.Context, r *vaultpb.ListManifestsRequest) (*vaultpb.ListManifestsResponse, error) {
	list, err := s.p.ListManifests(ctx, r)
	return &vaultpb.ListManifestsResponse{Manifests: list}, toStatus(err)
}

func (s *grpcServer) MerkleTree(ctx context.Context, r *vaultpb.MerkleTreeRequest) (*vaultpb.MerkleTreeResponse, error) {
	leaves, err := s.p.MerkleTree(ctx, r.Peer)
	return &vaultpb.MerkleTreeResponse{Leaves: leaves}, toStatus(err)
}

func (s *grpcServer) LeafEntries(ctx context.Context, r *vaultpb.LeafEntriesRequest) (*vaultpb.LeafEntriesResponse, error) {
	e, err := s.p.LeafEntries(ctx, r.Peer, r.Leaf)
	return &vaultpb.LeafEntriesResponse{Entries: e}, toStatus(err)
}

func (s *grpcServer) SyncCatalog(ctx context.Context, r *vaultpb.SyncCatalogRequest) (*vaultpb.SyncCatalogResponse, error) {
	c, err := s.p.SyncCatalog(ctx, r.Catalog)
	return &vaultpb.SyncCatalogResponse{Catalog: c}, toStatus(err)
}

func (s *grpcServer) Join(ctx context.Context, r *vaultpb.JoinRequest) (*vaultpb.JoinResponse, error) {
	if s.join == nil {
		return nil, status.Error(codes.Unimplemented, "join disabled")
	}
	resp, err := s.join(ctx, r)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	return resp, nil
}

// GRPCDialer maintains one client connection per peer address.
type GRPCDialer struct {
	tls   *tls.Config
	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// NewGRPCDialer returns a dialer that authenticates with tlsCfg. The server
// name is set per peer to its node ID, which must appear in its certificate.
func NewGRPCDialer(tlsCfg *tls.Config) *GRPCDialer {
	return &GRPCDialer{tls: tlsCfg, conns: map[string]*grpc.ClientConn{}}
}

// Dial implements Dialer.
func (d *GRPCDialer) Dial(m membership.Member) (Peer, error) {
	if m.RPCAddr == "" {
		return nil, fmt.Errorf("%w: %s has no rpc address", ErrUnavailable, m.ID)
	}
	key := m.ID + "@" + m.RPCAddr
	d.mu.Lock()
	defer d.mu.Unlock()
	if cc, ok := d.conns[key]; ok {
		return &grpcPeer{c: vaultpb.NewNodeClient(cc)}, nil
	}
	cfg := d.tls.Clone()
	cfg.ServerName = m.ID
	cc, err := grpc.NewClient(m.RPCAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes), grpc.MaxCallSendMsgSize(MaxMessageBytes)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: dial %s: %v", ErrUnavailable, m.RPCAddr, err)
	}
	d.conns[key] = cc
	return &grpcPeer{c: vaultpb.NewNodeClient(cc)}, nil
}

// Close closes every connection.
func (d *GRPCDialer) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, cc := range d.conns {
		_ = cc.Close()
		delete(d.conns, k)
	}
}

type grpcPeer struct{ c vaultpb.NodeClient }

func (p *grpcPeer) Ping(ctx context.Context) (string, error) {
	r, err := p.c.Ping(ctx, &vaultpb.PingRequest{})
	if err != nil {
		return "", fromStatus(err)
	}
	return r.NodeId, nil
}

func (p *grpcPeer) PutShard(ctx context.Context, hash string, data []byte) error {
	_, err := p.c.PutShard(ctx, &vaultpb.PutShardRequest{Hash: hash, Data: data})
	return fromStatus(err)
}

func (p *grpcPeer) GetShard(ctx context.Context, hash string) ([]byte, error) {
	r, err := p.c.GetShard(ctx, &vaultpb.GetShardRequest{Hash: hash})
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Data, nil
}

func (p *grpcPeer) HasShards(ctx context.Context, hashes []string, verify bool) ([]bool, error) {
	r, err := p.c.HasShards(ctx, &vaultpb.HasShardsRequest{Hashes: hashes, Verify: verify})
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Present, nil
}

func (p *grpcPeer) PutManifest(ctx context.Context, m *vaultpb.Manifest, hintFor string) (bool, error) {
	r, err := p.c.PutManifest(ctx, &vaultpb.PutManifestRequest{Manifest: m, HintFor: hintFor})
	if err != nil {
		return false, fromStatus(err)
	}
	return r.Applied, nil
}

func (p *grpcPeer) GetManifest(ctx context.Context, bucket, key string) (*vaultpb.Manifest, error) {
	r, err := p.c.GetManifest(ctx, &vaultpb.GetManifestRequest{Bucket: bucket, Key: key})
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Manifest, nil
}

func (p *grpcPeer) ListManifests(ctx context.Context, req *vaultpb.ListManifestsRequest) ([]*vaultpb.Manifest, error) {
	r, err := p.c.ListManifests(ctx, req)
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Manifests, nil
}

func (p *grpcPeer) MerkleTree(ctx context.Context, peerID string) ([][]byte, error) {
	r, err := p.c.MerkleTree(ctx, &vaultpb.MerkleTreeRequest{Peer: peerID})
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Leaves, nil
}

func (p *grpcPeer) LeafEntries(ctx context.Context, peerID string, leaf uint32) ([]*vaultpb.KeyVersion, error) {
	r, err := p.c.LeafEntries(ctx, &vaultpb.LeafEntriesRequest{Peer: peerID, Leaf: leaf})
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Entries, nil
}

func (p *grpcPeer) SyncCatalog(ctx context.Context, c *vaultpb.Catalog) (*vaultpb.Catalog, error) {
	r, err := p.c.SyncCatalog(ctx, &vaultpb.SyncCatalogRequest{Catalog: c})
	if err != nil {
		return nil, fromStatus(err)
	}
	return r.Catalog, nil
}

// JoinCluster calls Join on a seed node using a TLS config that pins the
// cluster CA fingerprint from the invite code.
func JoinCluster(ctx context.Context, addr string, tlsCfg *tls.Config, req *vaultpb.JoinRequest) (*vaultpb.JoinResponse, error) {
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("join: dial %s: %w", addr, err)
	}
	defer cc.Close()
	resp, err := vaultpb.NewNodeClient(cc).Join(ctx, req)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			return nil, fmt.Errorf("join: %s", st.Message())
		}
		return nil, fmt.Errorf("join: %w", err)
	}
	return resp, nil
}
