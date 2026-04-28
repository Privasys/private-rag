# private-rag

Per-fleet TDX enclave that hosts:

- conversations + messages (resumable across devices),
- chunked text + embeddings (pgvector) of documents the user has added to their data room,
- per-message feedback (good / bad / optional comment).

This repository currently contains the MCP tool schemas, a placeholder binary, and a reproducible build pipeline. The server itself is not yet implemented.

## Status

| Component | Status |
| --- | --- |
| MCP tool schemas (`internal/mcp`) | done |
| Reproducible OCI image build | done |
| In-memory store + REST API (`internal/store`, `internal/server`) | done |
| MCP HTTP transport (catalog + per-tool POST, stub data plane) | done |
| Postgres-backed store (auto-migrates schema on startup) | done |
| OIDC verifier (golang-jwt + JWKS via MicahParks/keyfunc) | done |
| Deployment recipe for Enclave OS Virtual | done (see [docs/deploy.md](docs/deploy.md)) |
| Real Tools implementation (pgvector + embeddings) | not started |
| Mutual RA-TLS pinning by MRTD (vs ai-gpu and enclave-cloud) | not started |
| Orchestrator wiring | not started |

## MCP surface

`internal/mcp/schema.go` is the source of truth for both the tools `private-rag` advertises and the inbound tools it consumes from `enclave-cloud`. The schemas are reflected to JSON Schema at server startup; do not maintain JSON Schemas by hand.

### Advertised by `private-rag` (used by the chat orchestrator)

- `search(query, top_k)` -> top-k chunks the caller can read in this fleet
- `fetch_chunk(chunk_id)` -> full chunk body + back reference to the original blob
- `list_documents()` -> documents indexed for the caller in this fleet
- `add_to_data_room(blob_uri)` -> register an `enclave-cloud` blob for indexing

### Consumed by `private-rag` (provided by `enclave-cloud`)

- `list_files(vault)` -> page through a vault
- `fetch_range(blob_uri, offset, length)` -> pull more context around a chunk
- `upload(metadata)` -> register a freshly-streamed blob

## Trust model

- Calls into `private-rag` are RA-TLS with mutual MRTD pinning. The orchestrator (`ai-gpu`) pins `private-rag`; `private-rag` pins `enclave-cloud`. None of the schemas above carry attestation material; that lives one layer down at the TLS handshake.
- The Privasys ID JWT `sub` claim is the unit of ownership. `private-rag` enforces that the calling `sub` owns every `conversation_id` it touches and that search results are filtered to documents the `sub` can read.
- Cross-`sub` sharing is OUT OF SCOPE for v1. Per-fleet shared knowledge bases require a fleet-level vault and are tracked separately.

## Test

```
go test ./internal/mcp
```

The current tests assert the registry stays in sync with the typed Args / Result structs, exercise the in-memory store (including cross-subject isolation), and round-trip every REST endpoint via httptest.

## REST surface

All paths are under `/api/v1` and require `Authorization: Bearer <jwt>`. The subject (`sub` claim) is the unit of ownership; cross-subject access returns `404 Not Found` to avoid leaking the existence of other users' rows.

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/v1/conversations` | List the caller's conversations |
| POST | `/api/v1/conversations` | Create a conversation |
| GET | `/api/v1/conversations/{id}` | Get one conversation |
| PATCH | `/api/v1/conversations/{id}` | Rename |
| DELETE | `/api/v1/conversations/{id}` | Delete (cascades to messages) |
| GET | `/api/v1/conversations/{id}/messages` | List messages |
| POST | `/api/v1/conversations/{id}/messages` | Append a message |
| PUT | `/api/v1/messages/{id}/feedback` | Upsert feedback (`good`/`bad` + optional comment) |
| GET | `/api/v1/messages/{id}/feedback` | Get the caller's latest feedback |

Health: `GET /healthz`, `GET /readyz` (no auth).

## MCP tool surface

The orchestrator (ai-gpu) advertises this catalog to the model on every chat completion and forwards each tool call to a single POST. Same auth model as REST: `Authorization: Bearer <jwt>`, subject scoped.

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/v1/mcp/tools` | Tool catalog with JSON-Schema input descriptors |
| POST | `/api/v1/mcp/tools/search` | Semantic search over the caller's chunks |
| POST | `/api/v1/mcp/tools/fetch_chunk` | Fetch the full body of a chunk by id |
| POST | `/api/v1/mcp/tools/list_documents` | Enumerate the caller's indexed documents |
| POST | `/api/v1/mcp/tools/add_to_data_room` | Add an enclave-cloud blob to the index |

The data plane (pgvector + embedding model) is not provisioned yet, so reads return empty results (so the orchestrator can advertise the tools without confusing the model) and writes return `503 tool not provisioned`. Once the Postgres-backed `mcp.Tools` implementation lands, the HTTP layer does not change.

## Run locally

```
docker compose up
```

The compose file at the repo root brings up Postgres + pgvector and the server with `--insecure-no-verify` for development.

Without Docker:

```
go run ./cmd/private-rag --listen :8443 --insecure-no-verify
```

`--insecure-no-verify` extracts the `sub` claim WITHOUT validating the JWT signature; it is for development only.

## Production configuration

Production deployments must set both:

- `--db-url` / `DATABASE_URL` - Postgres DSN (the schema is applied at startup)
- `--oidc-issuer` / `OIDC_ISSUER` - one or more issuer URLs (comma-separated). Discovery is performed at start, JWKS is cached and refreshed on unknown KIDs by [MicahParks/keyfunc](https://github.com/MicahParks/keyfunc) running over [golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt). We do not hand-roll JWT crypto.
- `--oidc-audience` / `OIDC_AUDIENCE` - one or more accepted aud values (comma-separated)

See [docs/deploy.md](docs/deploy.md) for a complete recipe (private-rag + colocated pgvector) targeting Enclave OS Virtual.

## Build

```
docker build -t private-rag:dev .
```

The image is intentionally reproducible: same source tree -> same `@sha256:...` digest. Operators pin that digest in the Enclave OS Virtual workload manifest, and clients verify it via the per-container OID extensions in the RA-TLS certificate. CI builds and pushes `ghcr.io/privasys/private-rag:latest` on every merge to `main`.
