package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ClaimsOnlyVerifier decodes the JWT claims WITHOUT verifying
// the signature and returns the `sub` claim. It is intended for
// the dev binary and unit tests; production deployments must
// configure a real verifier.
//
// Even without signature verification, this verifier still:
//   - validates the JWT shape (3 dot-separated base64url parts),
//   - rejects expired tokens (`exp` claim),
//   - rejects tokens missing `sub`.
//
// In production this is replaced by a JWKS-backed verifier that
// also checks `iss`, `aud`, signature, and role claims. The
// REST API contract does not change.
type ClaimsOnlyVerifier struct {
	// Now is the clock used for `exp` checks. nil means time.Now.
	Now func() time.Time
	// LogWarning controls whether each request logs that we are
	// running unverified. Defaults to true; tests set it to false.
	LogWarning bool
}

// Subject implements Verifier.
func (v *ClaimsOnlyVerifier) Subject(_ context.Context, bearer string) (string, error) {
	parts := strings.SplitN(bearer, ".", 3)
	if len(parts) != 3 {
		return "", errors.New("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("payload decode: %w", err)
	}
	var claims struct {
		Sub string  `json:"sub"`
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("payload parse: %w", err)
	}
	if claims.Sub == "" {
		return "", errors.New("missing sub claim")
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	if claims.Exp != 0 && now().Unix() > int64(claims.Exp) {
		return "", errors.New("token expired")
	}
	if v.LogWarning {
		log.Printf("server: WARN ClaimsOnlyVerifier accepted unverified token for sub=%q", claims.Sub)
	}
	return claims.Sub, nil
}

// StaticSubjectVerifier returns a fixed subject for any non-empty
// bearer token. Test-only.
type StaticSubjectVerifier struct {
	Sub string
}

// Subject implements Verifier.
func (v *StaticSubjectVerifier) Subject(_ context.Context, bearer string) (string, error) {
	if bearer == "" {
		return "", errors.New("empty token")
	}
	return v.Sub, nil
}
