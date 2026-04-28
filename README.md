# private-rag

Per-fleet TDX enclave that hosts:

- conversations + messages (resumable across devices),
- chunked text + embeddings (pgvector) of documents the user has added to their data room,
- per-message feedback (good / bad / optional comment).

This directory currently contains ONLY the MCP tool schemas (Phase 7.3 Task 2). The actual server is the next chunk of work (Phase 7.3 Task 3).

## Status

| Task | Status |
| --- | --- |
| Container skeleton (Postgres + pgvector + small CPU embedding model) | not started |
| MCP tool schemas (this directory) | done |
| Conversation + messages REST + MCP API | not started |
| Mutual RA-TLS pinning by MRTD (vs ai-gpu and enclave-cloud) | not started |
| Orchestrator wiring | not started |

See `.operations/plans/ai-plan.md` Section 7.3 for the full design.

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

The current tests assert the registry stays in sync with the typed Args / Result structs. They are not a substitute for integration tests (those land with Task 3).

## Build

```
docker build -t private-rag:dev .
```

The image is intentionally reproducible: same source tree -> same `@sha256:...` digest. Operators pin that digest in the Enclave OS Virtual workload manifest, and clients verify it via the per-container OID extensions in the RA-TLS certificate. CI builds and pushes `ghcr.io/privasys/private-rag:latest` on every merge to `main`.
