// Package server is the HTTP layer of private-rag.
//
// REST surface (all paths under /api/v1, all responses JSON):
//
//   GET    /api/v1/conversations                  list user's conversations
//   POST   /api/v1/conversations                  create a new conversation
//   GET    /api/v1/conversations/{id}             get one conversation
//   PATCH  /api/v1/conversations/{id}             rename
//   DELETE /api/v1/conversations/{id}             delete (and its messages)
//   GET    /api/v1/conversations/{id}/messages    list messages
//   POST   /api/v1/conversations/{id}/messages    append a message
//   PUT    /api/v1/messages/{id}/feedback         upsert feedback (good/bad/comment)
//   GET    /api/v1/messages/{id}/feedback         get the user's latest feedback
//
// Health endpoints (no auth):
//
//   GET    /healthz                               liveness
//   GET    /readyz                                readiness (store reachable)
//
// All API paths require an `Authorization: Bearer <jwt>` header
// from which the subject (`sub` claim) is extracted by the
// configured Verifier. Cross-subject access returns 404 by
// design, never 403, so the existence of other users' rows is
// never leaked.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/privasys/private-rag/internal/store"
)

// Verifier extracts and verifies the subject of a bearer token.
// Implementations live in subject.go.
type Verifier interface {
	Subject(ctx context.Context, bearer string) (sub string, err error)
}

// Config bundles the runtime dependencies of the HTTP server.
type Config struct {
	Store    store.Store
	Verifier Verifier
	// CORSOrigins is the list of allowed origins for browser
	// callers (typically the chat front-end). Empty disables
	// CORS entirely.
	CORSOrigins []string
}

// New builds an http.Handler with all routes wired up.
func New(cfg Config) http.Handler {
	if cfg.Store == nil {
		panic("server: Store is required")
	}
	if cfg.Verifier == nil {
		panic("server: Verifier is required")
	}
	mux := http.NewServeMux()

	// Health endpoints.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		// A trivial store probe: can we list for an empty subject? We
		// only care that the call doesn't hang or panic.
		_, err := cfg.Store.ListConversations(r.Context(), "readiness-probe")
		if err != nil && !errors.Is(err, store.ErrForbidden) {
			http.Error(w, "store not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	api := &apiHandlers{store: cfg.Store}

	mux.HandleFunc("GET /api/v1/conversations", api.listConversations)
	mux.HandleFunc("POST /api/v1/conversations", api.createConversation)
	mux.HandleFunc("GET /api/v1/conversations/{id}", api.getConversation)
	mux.HandleFunc("PATCH /api/v1/conversations/{id}", api.renameConversation)
	mux.HandleFunc("DELETE /api/v1/conversations/{id}", api.deleteConversation)
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", api.listMessages)
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", api.appendMessage)
	mux.HandleFunc("PUT /api/v1/messages/{id}/feedback", api.upsertFeedback)
	mux.HandleFunc("GET /api/v1/messages/{id}/feedback", api.getFeedback)

	var h http.Handler = mux
	h = authMiddleware(h, cfg.Verifier)
	if len(cfg.CORSOrigins) > 0 {
		h = corsMiddleware(h, cfg.CORSOrigins)
	}
	return h
}

// ---------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------

type ctxKey int

const subjectCtxKey ctxKey = 1

// subjectFromContext returns the authenticated subject for a
// request that has passed through authMiddleware.
func subjectFromContext(ctx context.Context) (string, bool) {
	sub, ok := ctx.Value(subjectCtxKey).(string)
	return sub, ok && sub != ""
}

func authMiddleware(next http.Handler, v Verifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health endpoints bypass auth.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		// CORS preflight handled by corsMiddleware before this point.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing Bearer token")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		sub, err := v.Subject(r.Context(), token)
		if err != nil || sub == "" {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), subjectCtxKey, sub)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func corsMiddleware(next http.Handler, allowed []string) http.Handler {
	allow := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		allow[strings.ToLower(o)] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if _, ok := allow[strings.ToLower(origin)]; ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------

type apiHandlers struct {
	store store.Store
}

func (a *apiHandlers) listConversations(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	cs, err := a.store.ListConversations(r.Context(), sub)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": cs})
}

func (a *apiHandlers) createConversation(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := a.store.CreateConversation(r.Context(), sub, body.Title)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *apiHandlers) getConversation(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	c, err := a.store.GetConversation(r.Context(), sub, r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *apiHandlers) renameConversation(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := a.store.RenameConversation(r.Context(), sub, r.PathValue("id"), body.Title)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *apiHandlers) deleteConversation(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	if err := a.store.DeleteConversation(r.Context(), sub, r.PathValue("id")); err != nil {
		mapError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *apiHandlers) listMessages(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	ms, err := a.store.ListMessages(r.Context(), sub, r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": ms})
}

func (a *apiHandlers) appendMessage(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	var body struct {
		Role    store.MessageRole `json:"role"`
		Content string            `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Role {
	case store.RoleUser, store.RoleAssistant, store.RoleSystem, store.RoleTool:
	default:
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	m, err := a.store.AppendMessage(r.Context(), sub, r.PathValue("id"), body.Role, body.Content)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (a *apiHandlers) upsertFeedback(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	var body struct {
		Rating  store.FeedbackRating `json:"rating"`
		Comment string               `json:"comment"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := a.store.UpsertFeedback(r.Context(), sub, r.PathValue("id"), body.Rating, body.Comment)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *apiHandlers) getFeedback(w http.ResponseWriter, r *http.Request) {
	sub, _ := subjectFromContext(r.Context())
	f, err := a.store.GetFeedback(r.Context(), sub, r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// ---------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("server: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict")
	default:
		log.Printf("server: store error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// shutdownTimeout is exported as a var so tests can shrink it.
var shutdownTimeout = 5 * time.Second

// Run starts the server and blocks until ctx is cancelled or
// ListenAndServe fails. It performs a graceful shutdown on
// cancellation.
func Run(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
