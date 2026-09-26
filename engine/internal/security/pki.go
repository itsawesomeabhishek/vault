// Package security manages cluster identity: the cluster certificate
// authority, per-node certificates for mutual TLS, shared secrets, and
// one-time invite tokens.
package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// File names inside the cluster directory.
const (
	caCertFile   = "ca.pem"
	caKeyFile    = "ca-key.pem"
	nodeCertFile = "node.pem"
	nodeKeyFile  = "node-key.pem"
	secretsFile  = "cluster.json"
)

// ErrNoCluster is returned when the node has not created or joined a cluster.
var ErrNoCluster = errors.New("node is not part of a cluster")

// Secrets are shared by every node in the cluster.
type Secrets struct {
	ClusterID string `json:"clusterId"`
	GossipKey []byte `json:"gossipKey"`
	DataKey   []byte `json:"dataKey"`
}

// Material is everything a node needs to talk to its cluster.
type Material struct {
	Secrets  Secrets
	CACert   *x509.Certificate
	CAKey    *ecdsa.PrivateKey
	CACertPE []byte
	CAKeyPE  []byte
	NodeCert tls.Certificate
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("security: random: %w", err)
	}
	return b, nil
}

// RandomHex returns n random bytes encoded as hex.
func RandomHex(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// NewClusterCA creates a new cluster: CA key pair and shared secrets.
func NewClusterCA() (secrets Secrets, caCertPEM, caKeyPEM []byte, err error) {
	id, err := RandomHex(8)
	if err != nil {
		return Secrets{}, nil, nil, err
	}
	gk, err := randomBytes(32)
	if err != nil {
		return Secrets{}, nil, nil, err
	}
	dk, err := randomBytes(32)
	if err != nil {
		return Secrets{}, nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Secrets{}, nil, nil, fmt.Errorf("security: ca key: %w", err)
	}
	sn, err := serial()
	if err != nil {
		return Secrets{}, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "Vault cluster " + id, Organization: []string{"Vault"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Secrets{}, nil, nil, fmt.Errorf("security: ca cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Secrets{}, nil, nil, fmt.Errorf("security: marshal ca key: %w", err)
	}
	return Secrets{ClusterID: id, GossipKey: gk, DataKey: dk},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, errors.New("security: invalid CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("security: parse CA: %w", err)
	}
	if keyPEM == nil {
		return cert, nil, nil
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("security: invalid CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("security: parse CA key: %w", err)
	}
	return cert, key, nil
}

// IssueNodeCert creates a certificate for nodeID signed by the CA. The node
// ID is both the common name (caller identity) and a DNS SAN (server name).
func IssueNodeCert(ca *x509.Certificate, caKey *ecdsa.PrivateKey, nodeID string, ips []net.IP) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("security: node key: %w", err)
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"Vault"}},
		DNSNames:     []string{nodeID},
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("security: sign node cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("security: marshal node key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// Fingerprint is the hex SHA-256 of a certificate's DER bytes.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Save writes cluster material into dir with owner-only permissions and
// issues a node certificate for nodeID.
func Save(dir, nodeID string, secrets Secrets, caCertPEM, caKeyPEM []byte, ips []net.IP) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("security: mkdir: %w", err)
	}
	ca, caKey, err := parseCA(caCertPEM, caKeyPEM)
	if err != nil {
		return err
	}
	certPEM, keyPEM, err := IssueNodeCert(ca, caKey, nodeID, ips)
	if err != nil {
		return err
	}
	sj, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return fmt.Errorf("security: marshal secrets: %w", err)
	}
	files := map[string][]byte{
		caCertFile: caCertPEM, caKeyFile: caKeyPEM, nodeCertFile: certPEM, nodeKeyFile: keyPEM, secretsFile: sj,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("security: write %s: %w", name, err)
		}
	}
	return nil
}

// Load reads cluster material from dir.
func Load(dir string) (*Material, error) {
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) }
	sj, err := read(secretsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCluster
	}
	if err != nil {
		return nil, fmt.Errorf("security: read secrets: %w", err)
	}
	var m Material
	if err := json.Unmarshal(sj, &m.Secrets); err != nil {
		return nil, fmt.Errorf("security: parse secrets: %w", err)
	}
	if m.CACertPE, err = read(caCertFile); err != nil {
		return nil, fmt.Errorf("security: read CA: %w", err)
	}
	if m.CAKeyPE, err = read(caKeyFile); err != nil {
		return nil, fmt.Errorf("security: read CA key: %w", err)
	}
	if m.CACert, m.CAKey, err = parseCA(m.CACertPE, m.CAKeyPE); err != nil {
		return nil, err
	}
	certPEM, err := read(nodeCertFile)
	if err != nil {
		return nil, fmt.Errorf("security: read node cert: %w", err)
	}
	keyPEM, err := read(nodeKeyFile)
	if err != nil {
		return nil, fmt.Errorf("security: read node key: %w", err)
	}
	if m.NodeCert, err = tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, fmt.Errorf("security: node key pair: %w", err)
	}
	m.NodeCert.Certificate = append(m.NodeCert.Certificate, m.CACert.Raw)
	return &m, nil
}

func (m *Material) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(m.CACert)
	return p
}

// ServerTLS accepts connections with or without a client certificate; the
// gRPC interceptor rejects unauthenticated calls to anything but Join.
func (m *Material) ServerTLS() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{m.NodeCert},
		ClientCAs:    m.pool(),
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
}

// ClientTLS authenticates this node to peers and verifies peers against the CA.
func (m *Material) ClientTLS() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{m.NodeCert},
		RootCAs:      m.pool(),
	}
}

// PinnedTLS returns a client config for joining: the server must present a
// chain containing a CA with the given fingerprint that signed its leaf.
func PinnedTLS(caFingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // verification is done in VerifyPeerCertificate via CA pinning
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			var ca *x509.Certificate
			certs := make([]*x509.Certificate, 0, len(raw))
			for _, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return fmt.Errorf("parse peer certificate: %w", err)
				}
				certs = append(certs, c)
				if Fingerprint(c) == caFingerprint {
					ca = c
				}
			}
			if ca == nil || len(certs) == 0 {
				return errors.New("server is not part of the invited cluster (CA fingerprint mismatch)")
			}
			pool := x509.NewCertPool()
			pool.AddCert(ca)
			_, err := certs[0].Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		},
	}
}
