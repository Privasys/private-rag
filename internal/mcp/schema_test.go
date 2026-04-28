// Package mcp_test compile-checks the MCP schemas and asserts
// the public tool registries stay in sync with the typed Args /
// Result structs declared in schema.go.
//
// This is intentionally schema-only: there is no MCP server in
// this repo yet. When the server lands, it must register exactly
// the tool names listed in PrivateRAGTools, with InputSchema
// generated from the matching Args struct.
package mcp

import "testing"

func TestRegistryShape(t *testing.T) {
	wantPrivate := map[string]bool{
		"search":           true,
		"fetch_chunk":      true,
		"list_documents":   true,
		"add_to_data_room": true,
	}
	if len(PrivateRAGTools) != len(wantPrivate) {
		t.Fatalf("PrivateRAGTools length drifted: got %d want %d",
			len(PrivateRAGTools), len(wantPrivate))
	}
	for _, name := range PrivateRAGTools {
		if !wantPrivate[name] {
			t.Errorf("unexpected private-rag tool %q", name)
		}
	}

	wantCloud := map[string]bool{
		"list_files":  true,
		"fetch_range": true,
		"upload":      true,
	}
	if len(EnclaveCloudTools) != len(wantCloud) {
		t.Fatalf("EnclaveCloudTools length drifted: got %d want %d",
			len(EnclaveCloudTools), len(wantCloud))
	}
	for _, name := range EnclaveCloudTools {
		if !wantCloud[name] {
			t.Errorf("unexpected enclave-cloud tool %q", name)
		}
	}
}

// TestSchemaCompileChecks ensures the Args / Result structs
// remain referenceable. Pure type touch - no behaviour asserted.
func TestSchemaCompileChecks(t *testing.T) {
	_ = SearchArgs{}
	_ = SearchResult{Hits: []SearchHit{{}}}
	_ = FetchChunkArgs{}
	_ = FetchChunkResult{}
	_ = ListDocumentsArgs{}
	_ = ListDocumentsResult{Documents: []DocumentSummary{{}}}
	_ = AddToDataRoomArgs{}
	_ = AddToDataRoomResult{}
	_ = ListFilesArgs{}
	_ = ListFilesResult{Files: []FileEntry{{}}}
	_ = FetchRangeArgs{}
	_ = FetchRangeResult{}
	_ = UploadArgs{}
	_ = UploadResult{}
	_ = Tool{}
}
