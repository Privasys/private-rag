// Package main is the entrypoint for the private-rag enclave server.
//
// At the moment this is a placeholder: the actual server (REST + MCP API
// over RA-TLS, backed by Postgres + pgvector) is not yet implemented. The
// placeholder exists so that the reproducible build pipeline (Dockerfile
// + .github/workflows/build.yml) has something to compile and pin a digest
// for.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is overwritten at build time via -ldflags='-X main.version=...'.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	fmt.Fprintln(os.Stderr, "private-rag: server not yet implemented")
	os.Exit(1)
}
