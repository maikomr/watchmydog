// Package sharedsecret implements ports.Authenticator for a single owner who
// logs in with one shared secret (ADR-0005).
//
// Sessions are stateless signed tokens:
//
//	v1.<base64url(JSON claims)>.<base64url(HMAC-SHA256(key, "v1."+claims))>
//
// The signing key is derived from the secret, so replacing the secret
// invalidates every existing session ("log out everywhere"). Nothing is
// stored server-side, so restarts do not log anyone out.
package sharedsecret

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/maikomr/watchmydog/internal/ports"
)

const (
	// MinSecretLength guards against trivially guessable secrets.
	MinSecretLength = 12

	tokenVersion = "v1"
	subject      = "owner"

	// Generated secrets use lowercase letters and digits without the
	// look-alikes 0/o, 1/l/i, so they are easy to type on a phone.
	alphabet     = "abcdefghjkmnpqrstuvwxyz23456789"
	secretGroups = 4
	groupLen     = 5
)

var b64 = base64.RawURLEncoding

type claims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// Authenticator checks the shared secret and issues/verifies sessions.
type Authenticator struct {
	key          []byte
	secretDigest []byte
	ttl          time.Duration
	now          func() time.Time
}

var _ ports.Authenticator = (*Authenticator)(nil)

// New builds an authenticator. now may be nil (uses time.Now).
func New(secret string, ttl time.Duration, now func() time.Time) (*Authenticator, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("secret must be at least %d characters", MinSecretLength)
	}
	if ttl <= 0 {
		return nil, errors.New("session ttl must be positive")
	}
	if now == nil {
		now = time.Now
	}
	key := mac([]byte(secret), []byte("watchmydog/session-key/"+tokenVersion))
	return &Authenticator{
		key: key,
		// Compare digests, not raw strings: constant time regardless of
		// input length, and the raw secret is not kept in memory.
		secretDigest: mac(key, []byte(secret)),
		ttl:          ttl,
		now:          now,
	}, nil
}

// Login returns a session when secret matches.
func (a *Authenticator) Login(_ context.Context, secret string) (ports.Session, error) {
	if !hmac.Equal(mac(a.key, []byte(secret)), a.secretDigest) {
		return ports.Session{}, ports.ErrInvalidCredentials
	}
	now := a.now()
	exp := now.Add(a.ttl).Unix()
	payload, err := json.Marshal(claims{Sub: subject, Iat: now.Unix(), Exp: exp})
	if err != nil {
		return ports.Session{}, fmt.Errorf("encode claims: %w", err)
	}
	body := tokenVersion + "." + b64.EncodeToString(payload)
	return ports.Session{
		Token:     body + "." + b64.EncodeToString(mac(a.key, []byte(body))),
		Principal: ports.Principal{Subject: subject},
		ExpiresAt: time.Unix(exp, 0),
	}, nil
}

// Verify checks the token's signature and expiry.
func (a *Authenticator) Verify(_ context.Context, token string) (ports.Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenVersion {
		return ports.Principal{}, ports.ErrInvalidSession
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac(a.key, []byte(parts[0]+"."+parts[1]))) {
		return ports.Principal{}, ports.ErrInvalidSession
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return ports.Principal{}, ports.ErrInvalidSession
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil || c.Sub == "" {
		return ports.Principal{}, ports.ErrInvalidSession
	}
	if a.now().Unix() >= c.Exp {
		return ports.Principal{}, ports.ErrInvalidSession
	}
	return ports.Principal{Subject: c.Sub}, nil
}

func mac(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// GenerateSecret returns a random secret like "k7m2p-x9adf-q3rtz-5hwnb"
// (20 characters from a 31-symbol alphabet, about 99 bits of entropy).
func GenerateSecret() (string, error) {
	var b strings.Builder
	n := big.NewInt(int64(len(alphabet)))
	for i := 0; i < secretGroups*groupLen; i++ {
		if i > 0 && i%groupLen == 0 {
			b.WriteByte('-')
		}
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("random: %w", err)
		}
		b.WriteByte(alphabet[idx.Int64()])
	}
	return b.String(), nil
}

// CreateSecretFile writes a newly generated secret to path with mode 0600.
// It refuses to overwrite an existing file so a secret is never replaced by
// accident; delete the file first to rotate.
func CreateSecretFile(path string) error {
	secret, err := GenerateSecret()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; delete it first to rotate the secret", path)
		}
		return err
	}
	if _, err := f.WriteString(secret + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
