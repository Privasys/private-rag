package mcp

import (
	"context"
	"errors"
)

// Tools is the data-plane interface backing the advertised MCP
// tool surface. The HTTP transport in `internal/server` calls
// these methods after the JWT subject has been extracted and
// the request body decoded into the typed Args.
//
// Implementations:
//
//   - NotProvisioned: returned today; embeddings and the
//     pgvector store have not been wired up yet, so search /
//     list return empty results and the mutating calls return
//     ErrNotProvisioned (mapped to HTTP 503 by the transport).
//   - A future Postgres-backed implementation will satisfy the
//     same interface without changes to the HTTP layer.
type Tools interface {
	Search(ctx context.Context, sub string, args SearchArgs) (SearchResult, error)
	FetchChunk(ctx context.Context, sub string, args FetchChunkArgs) (FetchChunkResult, error)
	ListDocuments(ctx context.Context, sub string, args ListDocumentsArgs) (ListDocumentsResult, error)
	AddToDataRoom(ctx context.Context, sub string, args AddToDataRoomArgs) (AddToDataRoomResult, error)
}

// ErrNotProvisioned is returned by the stub Tools implementation
// for any call that mutates server state. The transport maps it
// to 503 Service Unavailable so callers can distinguish "no
// data yet" (200 + empty list) from "ingestion not wired up"
// (503).
var ErrNotProvisioned = errors.New("mcp: tool not provisioned")

// ErrInvalidArgs is returned when the typed Args fail
// validation. The transport maps it to 400.
var ErrInvalidArgs = errors.New("mcp: invalid arguments")

// NotProvisioned is the default Tools implementation while the
// per-fleet pgvector + embedding model are still being plumbed.
// Read calls succeed and return empty results so the orchestrator
// can advertise the tools without the model thinking the call
// failed; write calls return ErrNotProvisioned so that any
// attempt to ingest documents surfaces clearly.
type NotProvisioned struct{}

func (NotProvisioned) Search(_ context.Context, _ string, _ SearchArgs) (SearchResult, error) {
	return SearchResult{Hits: []SearchHit{}}, nil
}

func (NotProvisioned) FetchChunk(_ context.Context, _ string, _ FetchChunkArgs) (FetchChunkResult, error) {
	return FetchChunkResult{}, ErrNotFound
}

func (NotProvisioned) ListDocuments(_ context.Context, _ string, _ ListDocumentsArgs) (ListDocumentsResult, error) {
	return ListDocumentsResult{Documents: []DocumentSummary{}}, nil
}

func (NotProvisioned) AddToDataRoom(_ context.Context, _ string, _ AddToDataRoomArgs) (AddToDataRoomResult, error) {
	return AddToDataRoomResult{}, ErrNotProvisioned
}

// ErrNotFound is the chunk-not-found sentinel; the transport
// maps it to 404 to avoid leaking cross-tenant existence.
var ErrNotFound = errors.New("mcp: not found")

// Catalog returns the advertised tool set with input schemas.
// The orchestrator includes this verbatim in the per-call tool
// list it sends to the model.
func Catalog() []Tool {
	return []Tool{
		{
			Name:        "search",
			Description: "Semantic search over documents the caller can read in this fleet's RAG index. Returns the top_k chunks ranked by cosine similarity. Use this BEFORE answering any question that may benefit from the user's uploaded documents.",
			InputSchema: searchSchema(),
		},
		{
			Name:        "fetch_chunk",
			Description: "Fetch the full body of a chunk by id. The chunk_id MUST come from a prior search hit. Use this when the snippet is not enough context.",
			InputSchema: fetchChunkSchema(),
		},
		{
			Name:        "list_documents",
			Description: "Enumerate the documents indexed for the calling user in this fleet. Useful when the user asks 'what do you have on file for me?' or similar.",
			InputSchema: listDocumentsSchema(),
		},
		{
			Name:        "add_to_data_room",
			Description: "Add an existing enclave-cloud blob (ec://<vault>/<sha256>) to this fleet's RAG index. Idempotent on (sub, source_blob_uri). Returns immediately; embeddings may still be in flight.",
			InputSchema: addToDataRoomSchema(),
		},
	}
}

func searchSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":     map[string]any{"type": "string", "minLength": 1},
			"top_k":     map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 5},
			"min_score": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func fetchChunkSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"chunk_id": map[string]any{"type": "string", "minLength": 1},
		},
		"required":             []string{"chunk_id"},
		"additionalProperties": false,
	}
}

func listDocumentsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cursor": map[string]any{"type": "string"},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
		},
		"additionalProperties": false,
	}
}

func addToDataRoomSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source_blob_uri": map[string]any{"type": "string", "pattern": "^ec://[^/]+/[0-9a-f]{64}$"},
			"title":           map[string]any{"type": "string"},
			"mime_type":       map[string]any{"type": "string"},
		},
		"required":             []string{"source_blob_uri"},
		"additionalProperties": false,
	}
}
