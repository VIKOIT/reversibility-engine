package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	// 2048 is the smallest size GitHub issues, and generating it per test is fast enough.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	return key
}

func pkcs1PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling PKCS#8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// GitHub has issued App keys in both encodings, and a key that "looks fine" but will not parse
// is a miserable thing to debug at deploy time.
func TestPrivateKeyAcceptsBothEncodings(t *testing.T) {
	t.Parallel()

	key := testKey(t)

	for name, encoded := range map[string][]byte{
		"PKCS#1": pkcs1PEM(t, key),
		"PKCS#8": pkcs8PEM(t, key),
	} {
		if _, err := NewAppAuthenticator(123, encoded); err != nil {
			t.Errorf("%s key was rejected: %v", name, err)
		}
	}
}

func TestPrivateKeyRejectsGarbage(t *testing.T) {
	t.Parallel()

	tests := map[string][]byte{
		"empty":              nil,
		"not PEM":            []byte("-----BEGIN NOTHING-----"),
		"PEM with junk body": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not a key")}),
	}

	for name, key := range tests {
		if _, err := NewAppAuthenticator(123, key); err == nil {
			t.Errorf("%s was accepted as a private key", name)
		}
	}
}

func TestAppIDIsRequired(t *testing.T) {
	t.Parallel()

	if _, err := NewAppAuthenticator(0, pkcs1PEM(t, testKey(t))); err == nil {
		t.Error("an authenticator with no app ID was created")
	}
}

// The assertion is what proves possession of the App's private key. If the signature does not
// verify, every API call fails with an opaque 401.
func TestSignedJWTVerifies(t *testing.T) {
	t.Parallel()

	key := testKey(t)

	auth, err := NewAppAuthenticator(4242, pkcs1PEM(t, key))
	if err != nil {
		t.Fatalf("NewAppAuthenticator: %v", err)
	}

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }

	token, err := auth.signJWT()
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}

	// The signature must verify against the public half of the key.
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding the signature: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, 5, digest[:], signature); err != nil {
		// 5 is crypto.SHA256.
		t.Fatalf("the assertion does not verify: %v", err)
	}

	var header map[string]string
	decodeSegment(t, parts[0], &header)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("header = %v, want RS256/JWT", header)
	}

	var claims map[string]any
	decodeSegment(t, parts[1], &claims)

	if got := int64(claims["iss"].(float64)); got != 4242 {
		t.Errorf("iss = %d, want 4242", got)
	}

	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))

	// Backdated to absorb clock skew: a token from the future is rejected outright.
	if iat >= now.Unix() {
		t.Errorf("iat %d is not backdated relative to %d", iat, now.Unix())
	}

	// GitHub refuses an assertion valid for more than ten minutes.
	if lifetime := time.Duration(exp-iat) * time.Second; lifetime > 10*time.Minute {
		t.Errorf("assertion lifetime is %s, over GitHub's ten-minute maximum", lifetime)
	}
	if exp <= now.Unix() {
		t.Errorf("assertion expires at %d, before the current time %d", exp, now.Unix())
	}
}

func decodeSegment(t *testing.T, segment string, into any) {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decoding segment: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshalling segment: %v", err)
	}
}

// Each signature must be fresh. A cached assertion would eventually expire and start failing.
func TestSignJWTProducesAFreshAssertionEachTime(t *testing.T) {
	t.Parallel()

	auth, err := NewAppAuthenticator(1, pkcs1PEM(t, testKey(t)))
	if err != nil {
		t.Fatalf("NewAppAuthenticator: %v", err)
	}

	first, err := auth.signJWT()
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}

	auth.now = func() time.Time { return time.Now().Add(time.Hour) }

	second, err := auth.signJWT()
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}

	if first == second {
		t.Error("the assertion did not change when the clock advanced")
	}
}

// A webhook with no installation ID cannot be authenticated for, and guessing one would act on
// a repository the App may not be installed in.
func TestInstallationTokenRequiresAnInstallationID(t *testing.T) {
	t.Parallel()

	auth, err := NewAppAuthenticator(1, pkcs1PEM(t, testKey(t)))
	if err != nil {
		t.Fatalf("NewAppAuthenticator: %v", err)
	}

	if _, err := auth.installationToken(context.Background(), 0); err == nil {
		t.Error("a token was minted for installation 0")
	}
}

// A cached token that is still comfortably valid must be reused, or a busy repository mints a
// new one on every push and burns through the App's rate limit.
func TestInstallationTokenUsesTheCache(t *testing.T) {
	t.Parallel()

	auth, err := NewAppAuthenticator(1, pkcs1PEM(t, testKey(t)))
	if err != nil {
		t.Fatalf("NewAppAuthenticator: %v", err)
	}

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }
	auth.tokens[99] = cachedToken{token: "cached-token", expires: now.Add(time.Hour)}

	got, err := auth.installationToken(context.Background(), 99)
	if err != nil {
		t.Fatalf("installationToken: %v", err)
	}
	if got != "cached-token" {
		t.Errorf("token = %q, want the cached one", got)
	}
}

// A token about to expire must not be handed out: a request that begins with four minutes left
// can easily finish with none.
func TestInstallationTokenRefreshesBeforeExpiry(t *testing.T) {
	t.Parallel()

	auth, err := NewAppAuthenticator(1, pkcs1PEM(t, testKey(t)))
	if err != nil {
		t.Fatalf("NewAppAuthenticator: %v", err)
	}

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }
	auth.tokens[99] = cachedToken{token: "nearly-expired", expires: now.Add(installationTokenMargin / 2)}

	// Minting will fail because there is no API to reach, but the point is that it was
	// attempted rather than the stale token being returned.
	got, err := auth.installationToken(context.Background(), 99)
	if err == nil && got == "nearly-expired" {
		t.Error("a token inside the refresh margin was reused")
	}
}

func TestStaticTokenFactory(t *testing.T) {
	t.Parallel()

	if _, err := StaticTokenFactory("")(context.Background(), 1); err == nil {
		t.Error("an empty token was accepted")
	}

	client, err := StaticTokenFactory("a-token")(context.Background(), 1)
	if err != nil {
		t.Fatalf("StaticTokenFactory: %v", err)
	}
	if client == nil {
		t.Error("no client was returned")
	}
}
