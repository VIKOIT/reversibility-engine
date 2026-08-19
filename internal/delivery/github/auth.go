package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	gh "github.com/google/go-github/v66/github"
)

// JWT lifetime bounds, from GitHub's App documentation.
//
// The maximum accepted is ten minutes. Nine is used so that a slow request still arrives with a
// valid token, and the issued-at is backdated a minute to absorb clock skew between this host
// and GitHub — a token rejected for being from the future is a confusing outage.
const (
	appTokenLifetime = 9 * time.Minute
	appTokenBackdate = 1 * time.Minute

	// installationTokens last an hour; they are refreshed early so a request never begins with
	// a token that expires mid-flight.
	installationTokenMargin = 5 * time.Minute
)

// StaticTokenFactory returns a ClientFactory that authenticates with one fixed token.
//
// This is the path for a personal access token or a pre-minted installation token: useful for
// self-hosting and for a single repository, and simple enough to be obviously correct.
func StaticTokenFactory(token string) ClientFactory {
	return func(ctx context.Context, _ int64) (*gh.Client, error) {
		if token == "" {
			return nil, errors.New("no GitHub token configured")
		}
		return gh.NewClient(nil).WithAuthToken(token), nil
	}
}

// AppAuthenticator mints per-installation clients for a GitHub App.
//
// A GitHub App does not hold a static credential. It signs a short-lived JWT with its private
// key, exchanges that for a token scoped to one installation, and repeats when the token
// expires. Tokens are cached per installation so that a busy repository does not mint a new one
// on every push.
type AppAuthenticator struct {
	appID      int64
	privateKey *rsa.PrivateKey

	mu     sync.Mutex
	tokens map[int64]cachedToken

	// now is injectable so expiry can be tested without sleeping.
	now func() time.Time
}

type cachedToken struct {
	token   string
	expires time.Time
}

// NewAppAuthenticator parses a PEM-encoded RSA private key and returns an authenticator.
func NewAppAuthenticator(appID int64, privateKeyPEM []byte) (*AppAuthenticator, error) {
	if appID == 0 {
		return nil, errors.New("github app: no app ID configured")
	}

	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}

	return &AppAuthenticator{
		appID:      appID,
		privateKey: key,
		tokens:     map[int64]cachedToken{},
		now:        time.Now,
	}, nil
}

// parsePrivateKey accepts both PKCS#1 and PKCS#8 encodings, because GitHub has issued App keys
// in both and a key that "looks fine" but will not parse is a miserable thing to debug.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("github app: private key is not valid PEM")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github app: private key is neither PKCS#1 nor PKCS#8: %w", err)
	}

	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github app: private key is %T, want an RSA key", parsed)
	}
	return key, nil
}

// ClientFactory returns a factory that mints installation-scoped clients.
func (a *AppAuthenticator) ClientFactory() ClientFactory {
	return func(ctx context.Context, installationID int64) (*gh.Client, error) {
		token, err := a.installationToken(ctx, installationID)
		if err != nil {
			return nil, err
		}
		return gh.NewClient(nil).WithAuthToken(token), nil
	}
}

// installationToken returns a cached token or mints a new one.
func (a *AppAuthenticator) installationToken(ctx context.Context, installationID int64) (string, error) {
	if installationID == 0 {
		return "", errors.New("github app: webhook payload carried no installation ID")
	}

	a.mu.Lock()
	cached, ok := a.tokens[installationID]
	a.mu.Unlock()

	if ok && a.now().Add(installationTokenMargin).Before(cached.expires) {
		return cached.token, nil
	}

	appJWT, err := a.signJWT()
	if err != nil {
		return "", err
	}

	client := gh.NewClient(nil).WithAuthToken(appJWT)

	token, _, err := client.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return "", fmt.Errorf("github app: minting a token for installation %d: %w", installationID, err)
	}

	a.mu.Lock()
	a.tokens[installationID] = cachedToken{token: token.GetToken(), expires: token.GetExpiresAt().Time}
	a.mu.Unlock()

	return token.GetToken(), nil
}

// signJWT builds the RS256 assertion that proves possession of the App's private key.
//
// This is written against the JWT wire format directly rather than pulling in a JWT library: the
// claim set is three fields and the signature is one RSA operation, and every dependency in this
// repository has to justify itself.
func (a *AppAuthenticator) signJWT() (string, error) {
	now := a.now()

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-appTokenBackdate).Unix(),
		"exp": now.Add(appTokenLifetime).Unix(),
		"iss": a.appID,
	}

	encodedHeader, err := encodeJWTSegment(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := encodeJWTSegment(claims)
	if err != nil {
		return "", err
	}

	signingInput := encodedHeader + "." + encodedClaims

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github app: signing the assertion: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeJWTSegment(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("github app: encoding the assertion: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
