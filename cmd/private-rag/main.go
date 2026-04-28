// Package main is the private-rag enclave server entrypoint.
//
// Configuration via flags or environment variables (the latter
// makes wiring through the enclave-os-virtual launcher painless,
// since LoadRequest.Env carries them verbatim into the container):
//
//   --listen / PRIVATE_RAG_LISTEN
//   --cors-origins / PRIVATE_RAG_CORS_ORIGINS
//   --db-url / DATABASE_URL                 Postgres DSN; if empty, runs in-memory
//   --oidc-issuer / OIDC_ISSUER             comma-separated issuer URLs
//   --oidc-audience / OIDC_AUDIENCE         comma-separated accepted aud values
//   --insecure-no-verify                    dev only; bypasses signature verification
//
// Production deployments MUST set --oidc-issuer (and almost
// always --oidc-audience) and --db-url. --insecure-no-verify
// is a flag, not a config value, so it cannot be set by accident
// through env.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/privasys/private-rag/internal/server"
	"github.com/privasys/private-rag/internal/store"
)

// version is overwritten at build time via -ldflags='-X main.version=...'.
var version = "dev"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	listen := flag.String("listen", envOr("PRIVATE_RAG_LISTEN", ":8443"),
		"address to listen on")
	corsOrigins := flag.String("cors-origins", envOr("PRIVATE_RAG_CORS_ORIGINS", ""),
		"comma-separated list of allowed CORS origins (exact match)")
	dbURL := flag.String("db-url", envOr("DATABASE_URL", ""),
		"Postgres DSN; if empty, runs with the in-memory store (NOT durable)")
	oidcIssuer := flag.String("oidc-issuer", envOr("OIDC_ISSUER", ""),
		"comma-separated OIDC issuer URLs")
	oidcAudience := flag.String("oidc-audience", envOr("OIDC_AUDIENCE", ""),
		"comma-separated accepted JWT aud values")
	insecureNoVerify := flag.Bool("insecure-no-verify", false,
		"accept JWTs without signature verification (dev/testing only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- Store -----------------------------------------------------------
	var st store.Store
	var storeKind string
	if *dbURL != "" {
		pg, err := store.NewPostgresStore(ctx, *dbURL)
		if err != nil {
			log.Fatalf("private-rag: postgres init: %v", err)
		}
		defer pg.Close()
		st = pg
		storeKind = "postgres"
	} else {
		st = store.NewInMemoryStore()
		storeKind = "in-memory (NOT durable)"
	}

	// --- Verifier --------------------------------------------------------
	var verifier server.Verifier
	var verifierKind string
	switch {
	case *oidcIssuer != "":
		v, err := server.NewOIDCVerifier(ctx, server.OIDCConfig{
			Issuers:   splitCSV(*oidcIssuer),
			Audiences: splitCSV(*oidcAudience),
		})
		if err != nil {
			log.Fatalf("private-rag: oidc init: %v", err)
		}
		defer v.Close()
		verifier = v
		verifierKind = "oidc(" + *oidcIssuer + ")"
	case *insecureNoVerify:
		verifier = &server.ClaimsOnlyVerifier{LogWarning: true}
		verifierKind = "claims-only (INSECURE)"
	default:
		log.Println("private-rag: refusing to start without a verifier")
		log.Println("  set --oidc-issuer (and --oidc-audience), or pass --insecure-no-verify for dev")
		os.Exit(2)
	}

	cfg := server.Config{
		Store:       st,
		Verifier:    verifier,
		CORSOrigins: splitCSV(*corsOrigins),
	}

	h := server.New(cfg)

	log.Printf("private-rag %s listening on %s (store=%s, verifier=%s)",
		version, *listen, storeKind, verifierKind)
	if err := server.Run(ctx, *listen, h); err != nil {
		log.Fatalf("server: %v", err)
	}
}
