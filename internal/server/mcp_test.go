package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPCatalogEndpoint(t *testing.T) {
	h, _ := newTestServer(t, "u")
	resp, body := do(t, h, "GET", "/api/v1/mcp/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantNames := map[string]bool{
		"search": false, "fetch_chunk": false,
		"list_documents": false, "add_to_data_room": false,
	}
	for _, tl := range got.Tools {
		if tl.InputSchema == nil {
			t.Errorf("tool %q has nil schema", tl.Name)
		}
		if _, ok := wantNames[tl.Name]; ok {
			wantNames[tl.Name] = true
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("catalog missing tool %q", name)
		}
	}
}

func TestMCPSearchEmpty(t *testing.T) {
	h, _ := newTestServer(t, "u")
	resp, body := do(t, h, "POST", "/api/v1/mcp/tools/search",
		map[string]any{"query": "anything"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got struct {
		Hits []any `json:"hits"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Hits) != 0 {
		t.Fatalf("want 0 hits from stub, got %d", len(got.Hits))
	}
}

func TestMCPSearchRequiresQuery(t *testing.T) {
	h, _ := newTestServer(t, "u")
	resp, _ := do(t, h, "POST", "/api/v1/mcp/tools/search", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestMCPAddToDataRoomNotProvisioned(t *testing.T) {
	h, _ := newTestServer(t, "u")
	resp, _ := do(t, h, "POST", "/api/v1/mcp/tools/add_to_data_room",
		map[string]any{"source_blob_uri": "ec://v/" + repeat64('a')})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d (want 503 from stub)", resp.StatusCode)
	}
}

func TestMCPFetchChunkNotFound(t *testing.T) {
	h, _ := newTestServer(t, "u")
	resp, _ := do(t, h, "POST", "/api/v1/mcp/tools/fetch_chunk",
		map[string]any{"chunk_id": "missing"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestMCPUnknownTool(t *testing.T) {
	h, _ := newTestServer(t, "u")
	resp, _ := do(t, h, "POST", "/api/v1/mcp/tools/explode",
		map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestMCPRequiresAuth(t *testing.T) {
	h, _ := newTestServer(t, "u")
	req := httptest.NewRequest("GET", "/api/v1/mcp/tools", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rr.Code)
	}
}

func repeat64(c byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = c
	}
	return string(out)
}
