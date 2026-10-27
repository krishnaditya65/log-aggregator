package rbac

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token scheme: this package hand-rolls a minimal HMAC-SHA256 signed token
// rather than depending on github.com/golang-jwt/jwt, to keep internal/rbac
// free of any new go.mod dependency (this package must not modify
// go.mod/go.sum). The format intentionally mirrors JWS/JWT compact
// serialization so it is familiar and easy to swap for a real JWT library
// later if desired:
//
//	base64url(header) "." base64url(payload) "." base64url(HMAC-SHA256(header+"."+payload, secret))
//
// header is always {"alg":"HS256","typ":"RBAC"}. payload is the JSON
// encoding of tokenClaims below. Verification uses constant-time comparison
// of the computed signature (crypto/subtle) and checks the "exp" claim
// against the current time.

var (
	// ErrInvalidToken is returned for malformed tokens or signature mismatch.
	ErrInvalidToken = errors.New("rbac: invalid token")
	// ErrExpiredToken is returned when a token's expiry has passed.
	ErrExpiredToken = errors.New("rbac: token expired")
)

const tokenHeader = `{"alg":"HS256","typ":"RBAC"}`

// tokenClaims is the JSON payload embedded in issued tokens.
type tokenClaims struct {
	UserID   string `json:"sub"`
	TenantID string `json:"tenant_id"`
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`
}

func b64encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func sign(secret []byte, headerB64, payloadB64 string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(headerB64 + "." + payloadB64))
	return b64encode(mac.Sum(nil))
}

// IssueToken creates a signed, time-limited bearer token for userID scoped
// to tenantID, valid for ttl from now.
func IssueToken(secret []byte, userID, tenantID string, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("rbac: secret must not be empty")
	}
	if userID == "" {
		return "", errors.New("rbac: userID must not be empty")
	}
	if tenantID == "" {
		return "", errors.New("rbac: tenantID must not be empty")
	}
	now := time.Now()
	claims := tokenClaims{
		UserID:   userID,
		TenantID: tenantID,
		IssuedAt: now.Unix(),
		Expiry:   now.Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("rbac: marshal claims: %w", err)
	}

	headerB64 := b64encode([]byte(tokenHeader))
	payloadB64 := b64encode(payload)
	sigB64 := sign(secret, headerB64, payloadB64)

	return headerB64 + "." + payloadB64 + "." + sigB64, nil
}

// VerifyToken validates a token's signature and expiry and returns the
// claims it carries as a Principal plus the issued-at time.
func VerifyToken(secret []byte, token string) (Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, ErrInvalidToken
	}
	headerB64, payloadB64, sigB64 := parts[0], parts[1], parts[2]

	wantSig := sign(secret, headerB64, payloadB64)
	if subtle.ConstantTimeCompare([]byte(wantSig), []byte(sigB64)) != 1 {
		return Principal{}, ErrInvalidToken
	}

	payload, err := b64decode(payloadB64)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Principal{}, ErrInvalidToken
	}
	if claims.UserID == "" || claims.TenantID == "" {
		return Principal{}, ErrInvalidToken
	}
	if time.Now().Unix() >= claims.Expiry {
		return Principal{}, ErrExpiredToken
	}

	return Principal{UserID: claims.UserID, TenantID: claims.TenantID}, nil
}
