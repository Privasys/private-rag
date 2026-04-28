package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/privasys/private-rag/internal/mcp"
)

// mcpHandlers wires the MCP tool data plane onto HTTP.
//
// Two endpoints, both under /api/v1/mcp:
//
//   GET  /api/v1/mcp/tools          catalog with input schemas
//   POST /api/v1/mcp/tools/{tool}   invoke a tool with JSON args
//
// JSON-RPC framing is intentionally NOT used here. The
// orchestrator (ai-gpu) translates each model tool call to a
// single POST and forwards the typed result back to the model;
// this keeps the wire shape easy to inspect with curl and lets
// the same endpoints back an MCP JSON-RPC adapter later if we
// need spec compliance for third-party MCP clients.
type mcpHandlers struct {
	tools mcp.Tools
}

func (m *mcpHandlers) listTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": mcp.Catalog()})
}

func (m *mcpHandlers) call(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	tool := r.PathValue("tool")
	switch tool {
	case "search":
		var args mcp.SearchArgs
		if err := decodeJSON(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if args.Query == "" {
			writeError(w, http.StatusBadRequest, "query is required")
			return
		}
		out, err := m.tools.Search(r.Context(), sub, args)
		if err != nil {
			mapMCPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)

	case "fetch_chunk":
		var args mcp.FetchChunkArgs
		if err := decodeJSON(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if args.ChunkID == "" {
			writeError(w, http.StatusBadRequest, "chunk_id is required")
			return
		}
		out, err := m.tools.FetchChunk(r.Context(), sub, args)
		if err != nil {
			mapMCPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)

	case "list_documents":
		var args mcp.ListDocumentsArgs
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &args); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		out, err := m.tools.ListDocuments(r.Context(), sub, args)
		if err != nil {
			mapMCPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)

	case "add_to_data_room":
		var args mcp.AddToDataRoomArgs
		if err := decodeJSON(r, &args); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if args.SourceBlobURI == "" {
			writeError(w, http.StatusBadRequest, "source_blob_uri is required")
			return
		}
		out, err := m.tools.AddToDataRoom(r.Context(), sub, args)
		if err != nil {
			mapMCPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)

	default:
		writeError(w, http.StatusNotFound, "unknown tool")
	}
}

func mapMCPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mcp.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, mcp.ErrInvalidArgs):
		writeError(w, http.StatusBadRequest, "invalid arguments")
	case errors.Is(err, mcp.ErrNotProvisioned):
		writeError(w, http.StatusServiceUnavailable, "tool not provisioned")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// Compile-time check: writeJSON returns no error to callers, so
// it does not need to participate in error mapping. Encoding
// failures are logged inside writeJSON.
var _ = json.Marshal
