package security

import (
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

func TestClusterMaterialRoundTripAndMutualTLS(t *testing.T) {
	secrets, caCert, caKey, err := NewClusterCA()
	if err != nil {
		t.Fatal(err)
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	if err := Save(dirA, "node-a", secrets, caCert, caKey, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	if err := Save(dirB, "node-b", secrets, caCert, caKey, nil); err != nil {
		t.Fatal(err)
	}
	a, err := Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if a.Secrets.ClusterID != secrets.ClusterID || len(a.Secrets.GossipKey) != 32 {
		t.Fatal("secrets not persisted")
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", a.ServerTLS())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- ""
			return
		}
		defer c.Close()
		tc := c.(*tls.Conn)
		if err := tc.Handshake(); err != nil {
			done <- ""
			return
		}
		st := tc.ConnectionState()
		if len(st.VerifiedChains) == 0 {
			done <- ""
			return
		}
		done <- st.VerifiedChains[0][0].Subject.CommonName
	}()
	cfg := b.ClientTLS()
	cfg.ServerName = "node-a"
	conn, err := tls.Dial("tcp", ln.Addr().String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := <-done; got != "node-b" {
		t.Fatalf("server saw caller %q", got)
	}
}

func TestPinnedTLSRejectsForeignCluster(t *testing.T) {
	s1, c1, k1, _ := NewClusterCA()
	_, c2, k2, _ := NewClusterCA()
	dir1, dir2 := t.TempDir(), t.TempDir()
	if err := Save(dir1, "n1", s1, c1, k1, nil); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir2, "n2", s1, c2, k2, nil); err != nil {
		t.Fatal(err)
	}
	m1, _ := Load(dir1)
	m2, _ := Load(dir2)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", m2.ServerTLS())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	if _, err := tls.Dial("tcp", ln.Addr().String(), PinnedTLS(Fingerprint(m1.CACert))); err == nil {
		t.Fatal("connection to a different cluster must fail")
	}
}

func TestInvitesAreSingleUseAndExpire(t *testing.T) {
	v := NewInvites()
	tok, _, err := v.Issue(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Redeem(tok); err != nil {
		t.Fatal(err)
	}
	if err := v.Redeem(tok); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("token reused")
	}
	now := time.Now()
	v.now = func() time.Time { return now }
	tok2, _, _ := v.Issue(time.Second)
	v.now = func() time.Time { return now.Add(2 * time.Second) }
	if err := v.Redeem(tok2); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("expired token accepted")
	}
}

func TestInviteCodec(t *testing.T) {
	inv := Invite{ClusterID: "c", Addr: "10.0.0.1:19000", Token: "t", CAFingerprint: "f", ExpiresUnix: time.Now().Add(time.Hour).Unix()}
	code, err := inv.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeInvite(" " + code + "\n")
	if err != nil || got != inv {
		t.Fatalf("decode: %+v %v", got, err)
	}
	for _, bad := range []string{"", "vault1.!!!", "other.abc"} {
		if _, err := DecodeInvite(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	inv.ExpiresUnix = time.Now().Add(-time.Minute).Unix()
	expired, _ := inv.Encode()
	if _, err := DecodeInvite(expired); err == nil {
		t.Fatal("expired invite accepted")
	}
}
