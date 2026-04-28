package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

// OIDCVerifier validates OIDC bearer tokens against one or more issuers.
// Each issuer's JWKS is discovered via the standard
// `/.well-known/openid-configuration` endpoint and cached in-process,
// with a rate-limited refresh on unknown KIDs.
//
// This is a thin adapter over `golang-jwt/jwt/v5` and
// `MicahParks/keyfunc/v3`, the same combination
// `platform/management-service` uses in production. We deliberately
// avoid hand-rolling JWT/JWKS code so signature handling stays
// in vetted upstream libraries.
//
// Implements server.Verifier.
type OIDCVerifier struct {
	issuers []*oidcIssuer
}

// OIDCConfig configures one or more issuers for an OIDCVerifier.
type OIDCConfig struct {
	// Issuers is a comma-separated list of OIDC issuer URLs (or a
	// pre-split slice; either is accepted).
	Issuers []string
	// Audiences is the list of accepted aud values; if empty,
	// audience validation is skipped (NOT recommended in prod).
	Audiences []string
	// HTTPTimeout is the discovery + JWKS fetch timeout.
	// Defaults to 10s.
	HTTPTimeout time.Duration
}

type oidcIssuer struct {
	issuer    string
	audiences []string
	jwks      keyfunc.Keyfunc
	cancel    context.CancelFunc
}

type oidcDiscovery struct {
	JWKSURI string `json:"jwks_uri"`
}

// NewOIDCVerifier builds a verifier that knows about every issuer in
// cfg.Issuers. Discovery is performed eagerly so a misconfigured
// issuer fails the process start rather than the first request.
func NewOIDCVerifier(ctx context.Context, cfg OIDCConfig) (*OIDCVerifier, error) {
	if len(cfg.Issuers) == 0 {
		return nil, fmt.Errorf("oidc: at least one issuer is required")
	}
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	httpc := &http.Client{Timeout: timeout}

	v := &OIDCVerifier{}
	for _, raw := range cfg.Issuers {
		iss := strings.TrimRight(strings.TrimSpace(raw), "/")
		if iss == "" {
			continue
		}
		jwksURI, err := discoverJWKS(httpc, iss)
		if err != nil {
			v.Close()
			return nil, fmt.Errorf("oidc: discover %s: %w", iss, err)
		}
		log.Printf("oidc: issuer=%s jwks=%s", iss, jwksURI)

		ictx, cancel := context.WithCancel(ctx)
		k, err := keyfunc.NewDefaultOverrideCtx(ictx, []string{jwksURI}, keyfunc.Override{
			RefreshUnknownKID: rate.NewLimiter(rate.Every(10*time.Second), 1),
		})
		if err != nil {
			cancel()
			v.Close()
			return nil, fmt.Errorf("oidc: jwks keyfunc for %s: %w", iss, err)
		}
		v.issuers = append(v.issuers, &oidcIssuer{
			issuer:    iss,
			audiences: append([]string(nil), cfg.Audiences...),
			jwks:      k,
			cancel:    cancel,
		})
	}
	if len(v.issuers) == 0 {
		return nil, fmt.Errorf("oidc: no usable issuers after parsing")
	}
	return v, nil
}

// Close stops the background JWKS refreshers.
func (v *OIDCVerifier) Close() {
	for _, ia := range v.issuers {
		if ia.cancel != nil {
			ia.cancel()
		}
	}
}

// Subject implements server.Verifier. It validates the bearer token
// against the matching issuer's JWKS and returns the `sub` claim.
func (v *OIDCVerifier) Subject(_ context.Context, token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("oidc: empty token")
	}

	candidates := v.issuers
	if iss, ok := unsafePeekIssuer(token); ok {
		for _, ia := range v.issuers {
			if strings.TrimRight(ia.issuer, "/") == strings.TrimRight(iss, "/") {
				candidates = []*oidcIssuer{ia}
				break
			}
		}
	}

	var lastErr error
	for _, ia := range candidates {
		sub, err := ia.verify(token)
		if err == nil {
			return sub, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("oidc: no issuer matched")
	}
	return "", lastErr
}

func (ia *oidcIssuer) verify(tokenStr string) (string, error) {
	parserOpts := []jwt.ParserOption{
		jwt.WithIssuer(ia.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30 * time.Second),
	}
	var (
		tok      *jwt.Token
		parseErr error
	)
	if len(ia.audiences) == 0 {
		tok, parseErr = jwt.Parse(tokenStr, ia.jwks.Keyfunc, parserOpts...)
	} else {
		for _, aud := range ia.audiences {
			opts := append(slices.Clone(parserOpts), jwt.WithAudience(aud))
			tok, parseErr = jwt.Parse(tokenStr, ia.jwks.Keyfunc, opts...)
			if parseErr == nil {
				break
			}
		}
	}
	if parseErr != nil {
		return "", fmt.Errorf("oidc: parse: %w", parseErr)
	}
	if !tok.Valid {
		return "", fmt.Errorf("oidc: token invalid")
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("oidc: unexpected claims type")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", fmt.Errorf("oidc: missing sub claim")
	}
	return sub, nil
}

func discoverJWKS(httpc *http.Client, issuer string) (string, error) {
	url := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	resp, err := httpc.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var d oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return "", err
	}
	if d.JWKSURI == "" {
		return "", fmt.Errorf("discovery missing jwks_uri")
	}
	return d.JWKSURI, nil
}

// unsafePeekIssuer decodes the JWT payload WITHOUT verifying the
// signature so we can route to the correct issuer's keyfunc. The
// returned issuer is then matched against our trusted list and the
// signature is verified by jwt.Parse.
func unsafePeekIssuer(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	// Use the unverified parser from golang-jwt to avoid copy-pasting
	// the base64 / claim decoding ourselves.
	p := jwt.NewParser()
	tok, _, err := p.ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return "", false
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", false
	}
	iss, _ := claims["iss"].(string)
	return iss, iss != ""
}
