package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Access-token lifetime and ID construction.
const (
	accessTokenTTL     = 10 * time.Minute
	refreshTokenTTL    = 30 * 24 * time.Hour
	authCodeTTL        = 10 * time.Minute
	accessTokenSubject = "ngodingmcp-user"
)

var (
	errInvalidToken = errors.New("invalid access token")
	errExpiredToken = errors.New("expired access token")
)

// accessTokenClaims is the JWT payload for an OAuth access token.
type accessTokenClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`
	Scope     string `json:"scope"`
	ID        string `json:"jti"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// parseSigningKey loads or creates the ES256 keypair from the persisted
// state. A missing signing key is not an error; it is generated and returned
// for the caller to save. A present but unusable one is an error.
func parseSigningKey(pemBytes string) (*ecdsa.PrivateKey, error) {
	if pemBytes == "" {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate signing key: %w", err)
		}
		return priv, nil
	}
	block, _ := pem.Decode([]byte(pemBytes))
	if block == nil {
		return nil, errors.New("oauth state: signingKey is not valid PEM")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse signing key: %w", err)
	}
	ec, ok := priv.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("oauth state: signing key is not ECDSA")
	}
	if ec.Curve != elliptic.P256() {
		return nil, errors.New("oauth state: signing key must be P-256")
	}
	return ec, nil
}

// marshalSigningKey renders a PKCS#8 PEM of the private key.
func marshalSigningKey(priv *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// signingKeyID derives a stable key id from the public key.
func signingKeyID(priv *ecdsa.PrivateKey) string {
	jwk, _ := publicJWK(priv)
	raw, _ := json.Marshal(jwk)
	sum := sha256.Sum256(raw)
	return "kid-" + base64.RawURLEncoding.EncodeToString(sum[:16])
}

func publicJWK(priv *ecdsa.PrivateKey) (map[string]string, error) {
	pub, ok := priv.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("signing key is not ECDSA")
	}
	// Bytes is the uncompressed point 0x04 || X || Y (32 bytes each for
	// P-256); it replaces the deprecated big.Int fields X and Y.
	raw, err := pub.Bytes()
	if err != nil {
		return nil, err
	}
	if len(raw) != 65 || raw[0] != 4 {
		return nil, errors.New("signing key is not an uncompressed P-256 point")
	}
	x, y := raw[1:33], raw[33:]
	return map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(x),
		"y":   base64.RawURLEncoding.EncodeToString(y),
	}, nil
}

// jwkWithUse returns the full JWK including use, alg and kid, for publication.
func jwkWithUse(priv *ecdsa.PrivateKey, kid string) map[string]string {
	jwk, err := publicJWK(priv)
	if err != nil {
		return nil
	}
	jwk["use"] = "sig"
	jwk["alg"] = "ES256"
	jwk["kid"] = kid
	return jwk
}

// issueAccessToken signs an ES256 access JWT for the resource identified by
// issuer (which is also the expected audience). The scope claim records what
// was granted to the client.
func issueAccessToken(priv *ecdsa.PrivateKey, kid, issuer, scope, resource string) (string, error) {
	now := time.Now()
	jti, err := randomID(16)
	if err != nil {
		return "", err
	}
	claims := accessTokenClaims{
		Issuer:    issuer,
		Subject:   accessTokenSubject,
		Audience:  resource,
		Scope:     scope,
		ID:        jti,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(accessTokenTTL).Unix(),
	}

	header := map[string]string{"typ": "at+jwt", "alg": "ES256", "kid": kid}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		return "", err
	}

	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// verifyAccessToken validates signature, expiry and audience (issuer).
func verifyAccessToken(token string, pub *ecdsa.PublicKey, issuer string) (*accessTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errInvalidToken
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, errInvalidToken
	}
	if header.Alg != "ES256" {
		return nil, errInvalidToken
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errInvalidToken
	}
	var claims accessTokenClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, errInvalidToken
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errInvalidToken
	}
	sigInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(sigInput))
	rVal := new(big.Int).SetBytes(sig[:32])
	sVal := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, sum[:], rVal, sVal) {
		return nil, errInvalidToken
	}

	now := time.Now().Unix()
	if claims.ExpiresAt <= now {
		return nil, errExpiredToken
	}
	if claims.Issuer != issuer {
		return nil, errInvalidToken
	}
	// RFC 8707: the audience may be the bare issuer origin (older clients,
	// empty resource) or the full /mcp endpoint (spec-compliant clients).
	switch claims.Audience {
	case issuer, issuer + "/mcp":
	default:
		return nil, errInvalidToken
	}
	return &claims, nil
}
