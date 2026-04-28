package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/privasys/private-rag/internal/store"
)

func newTestServer(t *testing.T, sub string) (http.Handler, *store.InMemoryStore) {
	t.Helper()
	st := store.NewInMemoryStore()
	h := New(Config{
		Store:    st,
		Verifier: &StaticSubjectVerifier{Sub: sub},
	})
	return h, st
}

func do(t *testing.T, h http.Handler, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Authorization", "Bearer test-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	resp := rr.Result()
	out, _ := readAll(t, resp)
	return resp, out
}

func readAll(t *testing.T, resp *http.Response) ([]byte, error) {
	t.Helper()
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

func TestHealthEndpoints(t *testing.T) {
	h, _ := newTestServer(t, "u")

	for _, p := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest("GET", p, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status=%d", p, rr.Code)
		}
	}
}

func TestRequiresBearer(t *testing.T) {
	h, _ := newTestServer(t, "u")
	req := httptest.NewRequest("GET", "/api/v1/conversations", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestConversationLifecycle(t *testing.T) {
	h, _ := newTestServer(t, "u")

	// Create.
	resp, body := do(t, h, "POST", "/api/v1/conversations", map[string]any{"title": "first"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, body)
	}
	var c struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.ID == "" || c.Title != "first" {
		t.Fatalf("bad create: %+v", c)
	}

	// List.
	resp, body = do(t, h, "GET", "/api/v1/conversations", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(body), c.ID) {
		t.Fatalf("list missing id: %s", body)
	}

	// Append messages.
	resp, _ = do(t, h, "POST", "/api/v1/conversations/"+c.ID+"/messages", map[string]any{"role": "user", "content": "hi"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("append user status=%d", resp.StatusCode)
	}
	resp, body = do(t, h, "POST", "/api/v1/conversations/"+c.ID+"/messages", map[string]any{"role": "assistant", "content": "hello"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("append asst status=%d body=%s", resp.StatusCode, body)
	}
	var asst struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &asst)

	// List messages.
	resp, body = do(t, h, "GET", "/api/v1/conversations/"+c.ID+"/messages", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list msgs status=%d", resp.StatusCode)
	}
	var lm struct {
		Messages []store.Message `json:"messages"`
	}
	_ = json.Unmarshal(body, &lm)
	if len(lm.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(lm.Messages))
	}

	// Feedback upsert + read.
	resp, _ = do(t, h, "PUT", "/api/v1/messages/"+asst.ID+"/feedback", map[string]any{"rating": "good", "comment": "thanks"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback status=%d", resp.StatusCode)
	}
	resp, body = do(t, h, "GET", "/api/v1/messages/"+asst.ID+"/feedback", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get feedback status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"good"`) {
		t.Fatalf("missing rating: %s", body)
	}

	// Bad role rejected.
	resp, _ = do(t, h, "POST", "/api/v1/conversations/"+c.ID+"/messages", map[string]any{"role": "bogus", "content": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad role status=%d", resp.StatusCode)
	}

	// Delete.
	resp, _ = do(t, h, "DELETE", "/api/v1/conversations/"+c.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
}

func TestCrossSubjectIsolation(t *testing.T) {
	st := store.NewInMemoryStore()
	hUser := New(Config{Store: st, Verifier: &StaticSubjectVerifier{Sub: "user-a"}})
	hAttacker := New(Config{Store: st, Verifier: &StaticSubjectVerifier{Sub: "attacker"}})

	// User creates a conversation.
	resp, body := do(t, hUser, "POST", "/api/v1/conversations", map[string]any{"title": "private"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", resp.StatusCode)
	}
	var c struct{ ID string }
	_ = json.Unmarshal(body, &c)

	// Attacker GETs it: must be 404, not 403.
	resp, _ = do(t, hAttacker, "GET", "/api/v1/conversations/"+c.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("attacker GET status=%d (want 404 to avoid existence leak)", resp.StatusCode)
	}

	// Attacker DELETE: also 404.
	resp, _ = do(t, hAttacker, "DELETE", "/api/v1/conversations/"+c.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("attacker DELETE status=%d", resp.StatusCode)
	}

	// Attacker LIST: empty.
	resp, body = do(t, hAttacker, "GET", "/api/v1/conversations", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attacker LIST status=%d", resp.StatusCode)
	}
	if strings.Contains(string(body), c.ID) {
		t.Fatalf("attacker saw victim's conversation: %s", body)
	}
}

func TestCORSPreflight(t *testing.T) {
	st := store.NewInMemoryStore()
	h := New(Config{
		Store:       st,
		Verifier:    &StaticSubjectVerifier{Sub: "u"},
		CORSOrigins: []string{"https://chat.privasys.org"},
	})
	req := httptest.NewRequest("OPTIONS", "/api/v1/conversations", nil)
	req.Header.Set("Origin", "https://chat.privasys.org")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://chat.privasys.org" {
		t.Fatalf("ACAO=%q", got)
	}
}

func TestClaimsOnlyVerifierExtractsSub(t *testing.T) {
	v := &ClaimsOnlyVerifier{LogWarning: false}
	// Hand-built JWT: header={"alg":"none"}, payload={"sub":"alice"}, sig=ignored.
	tok := "eyJhbGciOiJub25lIn0." +
		"eyJzdWIiOiJhbGljZSJ9." +
		"sig"
	sub, err := v.Subject(context.Background(), tok)
	if err != nil || sub != "alice" {
		t.Fatalf("got sub=%q err=%v", sub, err)
	}

	if _, err := v.Subject(context.Background(), "not-a-jwt"); err == nil {
		t.Fatalf("expected malformed error")
	}
}
