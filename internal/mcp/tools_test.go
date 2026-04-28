package mcp

import (
	"context"
	"errors"
	"testing"
)

func TestCatalogMatchesAdvertisedSet(t *testing.T) {
	got := map[string]bool{}
	for _, tl := range Catalog() {
		if tl.InputSchema == nil {
			t.Errorf("tool %q: InputSchema must not be nil", tl.Name)
		}
		if tl.Description == "" {
			t.Errorf("tool %q: Description must not be empty", tl.Name)
		}
		got[tl.Name] = true
	}
	for _, want := range PrivateRAGTools {
		if !got[want] {
			t.Errorf("Catalog missing advertised tool %q", want)
		}
	}
	if len(got) != len(PrivateRAGTools) {
		t.Errorf("Catalog has %d tools; PrivateRAGTools has %d (should match)",
			len(got), len(PrivateRAGTools))
	}
}

func TestNotProvisionedReadsReturnEmpty(t *testing.T) {
	var tools Tools = NotProvisioned{}
	ctx := context.Background()

	sr, err := tools.Search(ctx, "alice", SearchArgs{Query: "anything"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(sr.Hits) != 0 {
		t.Errorf("Search hits: want 0, got %d", len(sr.Hits))
	}

	lr, err := tools.ListDocuments(ctx, "alice", ListDocumentsArgs{})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(lr.Documents) != 0 {
		t.Errorf("ListDocuments: want 0, got %d", len(lr.Documents))
	}
}

func TestNotProvisionedWritesReturnSentinel(t *testing.T) {
	var tools Tools = NotProvisioned{}
	ctx := context.Background()

	_, err := tools.AddToDataRoom(ctx, "alice", AddToDataRoomArgs{SourceBlobURI: "ec://v/00"})
	if !errors.Is(err, ErrNotProvisioned) {
		t.Errorf("AddToDataRoom: want ErrNotProvisioned, got %v", err)
	}

	_, err = tools.FetchChunk(ctx, "alice", FetchChunkArgs{ChunkID: "x"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("FetchChunk: want ErrNotFound, got %v", err)
	}
}
