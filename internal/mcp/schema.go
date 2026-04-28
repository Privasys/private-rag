// Package mcp defines the MCP (Model Context Protocol) tool surface
// exposed by the per-fleet `private-rag` enclave and the inbound
// surface that `private-rag` consumes from `enclave-cloud`.
//
// This file is INTENTIONALLY schema-only (no implementation). It
// serves as the source of truth that:
//
//   - the chat orchestrator (ai-gpu) consults when advertising
//     tools to the LLM on every chat completion request;
//   - the `private-rag` Go server (Phase 7.3 Task 3) implements
//     against;
//   - integration tests use to assert request / response shape;
//   - the developer portal renders to document the per-fleet
//     RAG contract (Section 6.2 `privacy.mdx`).
//
// JSON-Schema compatibility: every Args / Result struct uses
// `json` tags ONLY, no nested anonymous structs, so the schema
// can be reflected to JSON Schema with a single pass at startup.
//
// Cross-component attestation is enforced at the RA-TLS layer
// (Phase 7.3 Task 4); these schemas assume the caller has
// already been pinned by MRTD and the JWT `sub` has been
// extracted.
package mcp

// Tool is the descriptor advertised to the LLM. Mirrors the
// MCP `Tool` envelope used by the model's function-calling
// surface; the orchestrator translates this to the vendor JSON
// shape (OpenAI / Anthropic / vLLM tool blocks).
type Tool struct {
	// Name is the wire name the model emits. Must be unique
	// within a single MCP server.
	Name string `json:"name"`
	// Description is shown to the model. Keep it terse and
	// describe the WHEN (preconditions / triggers), not the
	// HOW (implementation).
	Description string `json:"description"`
	// InputSchema is a JSON Schema (draft 2020-12) describing
	// the call arguments. Generated from the Args struct of
	// each tool below at server startup.
	InputSchema map[string]any `json:"inputSchema"`
}

// ---------------------------------------------------------------
// private-rag tools (server-side; advertised to the LLM)
// ---------------------------------------------------------------

// SearchArgs is the input to `search`. Returns the top_k chunks
// most similar to `query` from documents the calling `sub` can
// read in the active fleet.
//
// Filtering is enforced server-side using the JWT subject and
// the `enclave-cloud` vault membership snapshot; the caller
// MUST NOT filter client-side because top_k is applied AFTER
// the access filter.
type SearchArgs struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k,omitempty"` // default 5, max 20
	// MinScore optionally drops results below the threshold.
	// Cosine similarity, range [0, 1].
	MinScore float64 `json:"min_score,omitempty"`
}

// SearchHit is a single chunk match.
type SearchHit struct {
	ChunkID       string  `json:"chunk_id"`
	Score         float64 `json:"score"`
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title,omitempty"`
	// Snippet is a UTF-8 excerpt of the chunk text, capped at
	// ~512 chars. Use FetchChunk for the full body.
	Snippet string `json:"snippet"`
}

// SearchResult is the response to `search`.
type SearchResult struct {
	Hits []SearchHit `json:"hits"`
}

// FetchChunkArgs reads the full chunk body by id. The chunk_id
// must come from a SearchHit; calling this with an id the user
// cannot read returns a 404 (never 403, to avoid leaking the
// chunk's existence cross-tenant).
type FetchChunkArgs struct {
	ChunkID string `json:"chunk_id"`
}

// FetchChunkResult contains the verbatim chunk plus a back
// reference to the original blob so the agent can pull more
// context via `enclave-cloud.fetch_range`.
type FetchChunkResult struct {
	ChunkID    string `json:"chunk_id"`
	DocumentID string `json:"document_id"`
	// Text is the full chunk body (UTF-8).
	Text string `json:"text"`
	// SourceBlobURI follows the form ec://<vault>/<sha256>.
	SourceBlobURI string `json:"source_blob_uri"`
	// SourceOffset is the byte offset inside the original
	// blob where the chunk starts. Pair with len(Text) for
	// `enclave-cloud.fetch_range`.
	SourceOffset int64 `json:"source_offset"`
}

// ListDocumentsArgs has no fields today; reserved for future
// pagination.
type ListDocumentsArgs struct {
	// Cursor is opaque; clients should pass the value from a
	// prior ListDocumentsResult.NextCursor.
	Cursor string `json:"cursor,omitempty"`
	// Limit caps the page size (default 50, max 200).
	Limit int `json:"limit,omitempty"`
}

// DocumentSummary is the minimal listing shape; full metadata
// requires a (future) GetDocument tool.
type DocumentSummary struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	AddedAtUS  int64  `json:"added_at_us"`
	ChunkCount int    `json:"chunk_count"`
	// SourceBlobURI is the canonical reference back into the
	// user's enclave-cloud vault.
	SourceBlobURI string `json:"source_blob_uri"`
}

// ListDocumentsResult enumerates documents indexed for the
// caller's `sub` in the active fleet.
type ListDocumentsResult struct {
	Documents  []DocumentSummary `json:"documents"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// AddToDataRoomArgs adds an `enclave-cloud` blob to the fleet's
// RAG index. Idempotent on (sub, source_blob_uri).
//
// The actual ingestion (download via RA-TLS, chunk, embed,
// insert into pgvector) happens asynchronously; the call
// returns once the document row is committed.
type AddToDataRoomArgs struct {
	// SourceBlobURI must be ec://<vault>/<sha256> and the
	// caller must have read access to the blob.
	SourceBlobURI string `json:"source_blob_uri"`
	// Title is an optional friendly name; defaults to the
	// blob's filename metadata.
	Title string `json:"title,omitempty"`
	// MIMEType helps the chunker pick a strategy; if empty,
	// `private-rag` sniffs the blob.
	MIMEType string `json:"mime_type,omitempty"`
}

// AddToDataRoomResult returns the freshly-created document id;
// the embeddings may still be in flight when this returns.
type AddToDataRoomResult struct {
	DocumentID string `json:"document_id"`
	// Status is one of: "queued", "embedding", "ready".
	Status string `json:"status"`
}

// ---------------------------------------------------------------
// enclave-cloud tools (consumed BY private-rag, listed here so
// integration tests have a single source of truth)
// ---------------------------------------------------------------

// ListFilesArgs lists blobs in a vault the caller can read.
type ListFilesArgs struct {
	Vault  string `json:"vault"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// FileEntry is a single blob in a vault listing.
type FileEntry struct {
	BlobURI    string `json:"blob_uri"`
	Filename   string `json:"filename,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	MIMEType   string `json:"mime_type,omitempty"`
	ModifiedUS int64  `json:"modified_us"`
}

// ListFilesResult returns one page of vault entries.
type ListFilesResult struct {
	Files      []FileEntry `json:"files"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// FetchRangeArgs returns a byte range of a blob. Used by the
// agent when a chunk snippet is not enough context.
type FetchRangeArgs struct {
	BlobURI string `json:"blob_uri"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
}

// FetchRangeResult is base64-encoded so the MCP transport can
// carry binary safely; clients decode based on the blob's MIME.
type FetchRangeResult struct {
	BlobURI string `json:"blob_uri"`
	Offset  int64  `json:"offset"`
	// DataB64 is the requested range, base64-encoded.
	DataB64 string `json:"data_b64"`
}

// UploadArgs creates a new blob in the caller's vault. The
// upload itself uses a separate streaming endpoint; this MCP
// tool only registers metadata once the streamed body has been
// received.
type UploadArgs struct {
	Vault    string `json:"vault"`
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type,omitempty"`
	// SHA256Hex is the lowercase hex digest the upload
	// session emitted; enclave-cloud verifies it before the
	// blob becomes addressable.
	SHA256Hex string `json:"sha256_hex"`
	SizeBytes int64  `json:"size_bytes"`
}

// UploadResult is the canonical blob URI to use in subsequent
// `add_to_data_room` calls.
type UploadResult struct {
	BlobURI string `json:"blob_uri"`
}

// ---------------------------------------------------------------
// Tool registry (advertised set)
// ---------------------------------------------------------------

// PrivateRAGTools is the canonical advertised set for the
// private-rag MCP server. The orchestrator emits this list (with
// schemas filled in via reflection at startup) on every chat
// completion.
//
// Additions are NOT backwards-compatible at the model layer (the
// model may begin to call a new tool unprompted), so each new
// entry needs a coordinated rollout with the orchestrator's
// system prompt.
var PrivateRAGTools = []string{
	"search",
	"fetch_chunk",
	"list_documents",
	"add_to_data_room",
}

// EnclaveCloudTools is the inbound surface private-rag depends
// on. Listed here so integration tests can verify the
// enclave-cloud server keeps these tool names stable.
var EnclaveCloudTools = []string{
	"list_files",
	"fetch_range",
	"upload",
}
