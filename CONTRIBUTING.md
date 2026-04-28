# Contributing to private-rag

Thank you for your interest in contributing to private-rag.

`private-rag` is the per-fleet enclave that stores conversation history,
embedded document chunks, and per-message feedback for users of the Privasys
chat platform. It runs as an OCI container inside an Enclave OS Virtual TDX
Confidential VM and is reached over RA-TLS with mutual MRTD pinning.

## Getting Started

1. Fork and clone the repository
2. Install [Go 1.22+](https://go.dev/dl/)
3. Run tests: `go test ./...`

The image expected by Enclave OS Virtual is built from the `Dockerfile` at
the repo root. It must be reproducible: byte-for-byte identical layers from
the same source tree, so that the resulting `@sha256:...` digest can be
pinned in operator manifests and verified by clients via the per-container
OID extensions in the RA-TLS certificate.

## Project Structure

| Path | Description |
|------|-------------|
| `internal/mcp/` | MCP tool schemas advertised by private-rag and consumed from enclave-cloud |
| `Dockerfile` | Reproducible OCI image used by Enclave OS Virtual |
| `.github/workflows/build.yml` | CI: tests + reproducible image build |

## Making Changes

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages
- All commits must be GPG-signed
- Add tests for new functionality
- Do not introduce non-deterministic build steps (no embedded timestamps,
  no network fetches outside `go mod download`, no unpinned base images).
  The repository must produce the same OCI image digest on every build.

## Submitting a Pull Request

1. Create a feature branch from `main`
2. Make your changes with clear, focused commits
3. Ensure `go test ./...` passes
4. Open a PR against `main` with a description of the change

## Reporting Issues

Please use [GitHub Issues](https://github.com/Privasys/private-rag/issues) to
report bugs or request features. Security-sensitive reports must follow
[SECURITY.md](SECURITY.md) instead.

## License

By contributing, you agree that your contributions will be licensed under the
[GNU Affero General Public License v3.0](LICENSE).
