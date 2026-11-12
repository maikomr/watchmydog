package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

var b64 = base64.RawURLEncoding

// device plays the browser: it owns the keys of a subscription and can
// decrypt what the notifier sends, following RFC 8291.
type device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newDevice(t *testing.T) device {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return device{priv: priv, auth: auth}
}

func (d device) subscription(endpoint string) domain.PushSubscription {
	return domain.PushSubscription{
		Endpoint: endpoint,
		P256DH:   b64.EncodeToString(d.priv.PublicKey().Bytes()),
		Auth:     b64.EncodeToString(d.auth),
	}
}

func (d device) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	// Header: salt(16) | record size(4) | key length(1) | sender public key.
	if len(body) < 21 {
		t.Fatalf("body too short: %d bytes", len(body))
	}
	salt, keyLen := body[:16], int(body[20])
	senderPub, ciphertext := body[21:21+keyLen], body[21+keyLen:]

	pub, err := ecdh.P256().NewPublicKey(senderPub)
	if err != nil {
		t.Fatalf("sender key: %v", err)
	}
	shared, err := d.priv.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := "WebPush: info\x00" + string(d.priv.PublicKey().Bytes()) + string(senderPub)
	ikm, err := hkdf.Key(sha256.New, shared, d.auth, keyInfo, 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	// The plaintext ends with the delimiter 0x02 and optional zero padding.
	plain = []byte(strings.TrimRight(string(plain), "\x00"))
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("missing record delimiter in %q", plain)
	}
	return plain[:len(plain)-1]
}

// verifyVAPID checks the ES256 signature of the token in the Authorization
// header against the public key and returns its claims.
func verifyVAPID(t *testing.T, header, publicKey string) map[string]any {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	if !ok {
		t.Fatalf("Authorization = %q", header)
	}
	token, k, ok := strings.Cut(rest, ", k=")
	if !ok || k != publicKey {
		t.Fatalf("k = %q, want the public key %q", k, publicKey)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	rawKey, err := b64.DecodeString(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), rawKey) //nolint:staticcheck // fine in a test
	if x == nil {
		t.Fatal("public key is not a P-256 point")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %d bytes, err %v", len(sig), err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID token signature does not verify")
	}
	claimsJSON, err := b64.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func testKeys(t *testing.T) Keys {
	t.Helper()
	keys, _, err := LoadOrCreateKeys(filepath.Join(t.TempDir(), "vapid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestNotifySendsAMessageTheDeviceCanRead(t *testing.T) {
	var (
		gotHeader http.Header
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	keys := testKeys(t)
	n, err := New(Settings{Keys: keys, Contact: "mailto:someone@example.com", TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if n.PublicKey() != keys.Public {
		t.Error("PublicKey() differs from the key file")
	}
	dev := newDevice(t)
	at := time.Date(2026, 11, 12, 14, 32, 0, 0, time.UTC)
	msg := domain.Notification{Title: "Noise in Living room", Body: "14:32 · -21 dB", URL: "/?camera=dog", Tag: "sound-dog", At: at}
	if err := n.Notify(context.Background(), dev.subscription(srv.URL+"/send/abc"), msg); err != nil {
		t.Fatal(err)
	}

	if got := gotHeader.Get("Content-Encoding"); got != "aes128gcm" {
		t.Errorf("Content-Encoding = %q", got)
	}
	if got := gotHeader.Get("TTL"); got != "600" {
		t.Errorf("TTL = %q, want 600", got)
	}
	if got := gotHeader.Get("Urgency"); got != "high" {
		t.Errorf("Urgency = %q, want high", got)
	}
	claims := verifyVAPID(t, gotHeader.Get("Authorization"), keys.Public)
	if claims["aud"] != srv.URL {
		t.Errorf("aud = %v, want %s", claims["aud"], srv.URL)
	}
	if claims["sub"] != "mailto:someone@example.com" {
		t.Errorf("sub = %v", claims["sub"])
	}

	if strings.Contains(string(gotBody), "Living room") {
		t.Fatal("the push service can read the message: it is not encrypted")
	}
	var p Payload
	if err := json.Unmarshal(dev.decrypt(t, gotBody), &p); err != nil {
		t.Fatal(err)
	}
	want := Payload{Title: msg.Title, Body: msg.Body, URL: msg.URL, Tag: msg.Tag, TS: at.UnixMilli()}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

func TestNotifyMapsPushServiceAnswers(t *testing.T) {
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, "details from the push service")
	}))
	defer srv.Close()
	n, err := New(Settings{Keys: testKeys(t), Contact: "https://example.com/contact"})
	if err != nil {
		t.Fatal(err)
	}
	sub := newDevice(t).subscription(srv.URL)

	for _, c := range []struct {
		status int
		gone   bool
		ok     bool
	}{
		{http.StatusOK, false, true},
		{http.StatusCreated, false, true},
		{http.StatusNotFound, true, false},
		{http.StatusGone, true, false},
		{http.StatusForbidden, false, false},
		{http.StatusTooManyRequests, false, false},
		{http.StatusInternalServerError, false, false},
	} {
		status = c.status
		err := n.Notify(context.Background(), sub, domain.Notification{Title: "t"})
		switch {
		case c.ok && err != nil:
			t.Errorf("%d: unexpected error %v", c.status, err)
		case c.gone && !errors.Is(err, ports.ErrSubscriptionGone):
			t.Errorf("%d: err = %v, want ErrSubscriptionGone", c.status, err)
		case !c.ok && !c.gone:
			if err == nil || errors.Is(err, ports.ErrSubscriptionGone) || !strings.Contains(err.Error(), "details from the push service") {
				t.Errorf("%d: err = %v, want an error quoting the answer", c.status, err)
			}
		}
	}
}

func TestNotifyFailsOnUnreachableServiceAndBadKeys(t *testing.T) {
	n, err := New(Settings{Keys: testKeys(t), Contact: "a@example.com", HTTPClient: &http.Client{Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if err := n.Notify(context.Background(), newDevice(t).subscription(url), domain.Notification{}); err == nil {
		t.Error("no error for an unreachable push service")
	}
	bad := domain.PushSubscription{Endpoint: url, P256DH: "AAAA", Auth: "AAAA"}
	if err := n.Notify(context.Background(), bad, domain.Notification{}); err == nil {
		t.Error("no error for a subscription with a bad key")
	}
}

func TestNewRequiresKeysAndContact(t *testing.T) {
	if _, err := New(Settings{Contact: "a@example.com"}); err == nil {
		t.Error("accepted missing keys")
	}
	if _, err := New(Settings{Keys: Keys{Public: "p", Private: "s"}}); err == nil {
		t.Error("accepted missing contact")
	}
}

func TestLoadOrCreateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "vapid.json")
	first, created, err := LoadOrCreateKeys(path)
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	if raw, err := b64.DecodeString(first.Public); err != nil || len(raw) != 65 {
		t.Errorf("public key: %d bytes, err %v; want 65", len(raw), err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}

	second, created, err := LoadOrCreateKeys(path)
	if err != nil || created {
		t.Fatalf("second call: created=%v err=%v", created, err)
	}
	if second != first {
		t.Error("second call returned different keys")
	}

	for name, content := range map[string]string{"broken": "{not json", "empty": `{"publicKey":""}`} {
		p := filepath.Join(t.TempDir(), name+".json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadOrCreateKeys(p); err == nil {
			t.Errorf("%s key file accepted", name)
		}
		if got, _ := os.ReadFile(p); string(got) != content {
			t.Errorf("%s key file was overwritten", name)
		}
	}
}
