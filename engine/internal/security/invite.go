package security

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// InvitePrefix marks Vault invite codes.
const InvitePrefix = "vault1."

// Invite is the decoded content of an invite code.
type Invite struct {
	ClusterID     string `json:"c"`
	Addr          string `json:"a"`
	Token         string `json:"t"`
	CAFingerprint string `json:"f"`
	ExpiresUnix   int64  `json:"e"`
}

// Encode renders the invite as a copy-pasteable string.
func (i Invite) Encode() (string, error) {
	b, err := json.Marshal(i)
	if err != nil {
		return "", fmt.Errorf("security: encode invite: %w", err)
	}
	return InvitePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ErrInvalidInvite is returned for malformed, expired or reused invites.
var ErrInvalidInvite = errors.New("invalid or expired invite")

// DecodeInvite parses an invite code.
func DecodeInvite(code string) (Invite, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, InvitePrefix) || len(code) > 4096 {
		return Invite{}, ErrInvalidInvite
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, InvitePrefix))
	if err != nil {
		return Invite{}, ErrInvalidInvite
	}
	var inv Invite
	if err := json.Unmarshal(raw, &inv); err != nil || inv.Addr == "" || inv.Token == "" || inv.CAFingerprint == "" {
		return Invite{}, ErrInvalidInvite
	}
	if time.Now().Unix() > inv.ExpiresUnix {
		return Invite{}, fmt.Errorf("%w: expired", ErrInvalidInvite)
	}
	return inv, nil
}

// Invites issues and redeems single-use join tokens. Only token hashes are
// kept in memory, so a heap dump does not reveal redeemable tokens.
type Invites struct {
	mu     sync.Mutex
	tokens map[[32]byte]time.Time
	now    func() time.Time
}

// NewInvites returns an empty token registry.
func NewInvites() *Invites {
	return &Invites{tokens: map[[32]byte]time.Time{}, now: time.Now}
}

// Issue creates a token valid for ttl.
func (v *Invites) Issue(ttl time.Duration) (token string, expires time.Time, err error) {
	token, err = RandomHex(24)
	if err != nil {
		return "", time.Time{}, err
	}
	expires = v.now().Add(ttl)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.gc()
	v.tokens[sha256.Sum256([]byte(token))] = expires
	return token, expires, nil
}

// Redeem consumes a token. It succeeds at most once per token.
func (v *Invites) Redeem(token string) error {
	h := sha256.Sum256([]byte(token))
	v.mu.Lock()
	defer v.mu.Unlock()
	exp, ok := v.tokens[h]
	delete(v.tokens, h)
	if !ok || v.now().After(exp) {
		return ErrInvalidInvite
	}
	return nil
}

func (v *Invites) gc() {
	now := v.now()
	for h, exp := range v.tokens {
		if now.After(exp) {
			delete(v.tokens, h)
		}
	}
}
