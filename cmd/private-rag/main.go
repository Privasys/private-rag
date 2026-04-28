// Package main is the private-rag enclave server entrypoint.
//
// In its current form the server runs an in-memory store: it
// satisfies the REST contract used by the chat front-end and is
// suitable for development and integration tests, but it is NOT
// durable. Restarting the process drops all conversations and
// feedback. The Postgres + pgvector backend is the next major
// piece of work and will plug into the same store.Store
// interface without changing this entrypoint.
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

func main() {
	listen := flag.String("listen", ":8443", "address to listen on")
	corsOrigins := flag.String("cors-origins", "", "comma-separated list of allowed CORS origins (exact match)")
	insecureNoVerify := flag.Bool("insecure-no-verify", false,
		"accept JWTs without signature verification (dev/testing only). "+
			"In production, deploy behind RA-TLS and configure a real OIDC verifier.")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if !*insecureNoVerify {
		log.Println("private-rag: refusing to start without a verifier")
		log.Println("  pass --insecure-no-verify for dev mode, or wait for the JWKS verifier integration")
		os.Exit(2)
	}

	st := store.NewInMemoryStore()
	verifier := &server.ClaimsOnlyVerifier{LogWarning: true}

	cfg := server.Config{
		Store:    st,
		Verifier: verifier,
	}
	if *corsOrigins != "" {
		for _, o := range strings.Split(*corsOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.CORSOrigins = append(cfg.CORSOrigins, o)
			}
		}
	}

	h := server.New(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("private-rag %s listening on %s (in-memory store, claims-only verifier)", version, *listen)
	if err := server.Run(ctx, *listen, h); err != nil {
		log.Fatalf("server: %v", err)
	}
}
