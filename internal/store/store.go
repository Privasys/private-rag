// Package store defines the persistence interface used by the
// private-rag REST and MCP servers, plus an in-memory
// implementation used for tests and the dev binary.
//
// The on-disk implementation (Postgres + pgvector) lives in a
// follow-up. Both implementations MUST satisfy the Store
// interface so the server can swap them via configuration.
//
// All access is scoped by `sub` (the JWT subject). The store
// itself is the enforcement point: callers cannot read or write
// rows owned by a different subject.
package store

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Errors returned by the store. The server maps these to HTTP
// status codes.
var (
	ErrNotFound  = errors.New("store: not found")
	ErrForbidden = errors.New("store: forbidden")
	ErrConflict  = errors.New("store: conflict")
)

// Conversation is a chat thread owned by a single subject.
type Conversation struct {
	ID        string    `json:"id"`
	Sub       string    `json:"-"` // never leaks over the wire; routing key
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MessageRole is the originator of a chat message.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
	RoleTool      MessageRole = "tool"
)

// Message is a single turn inside a Conversation. Content is
// kept opaque (UTF-8 string) because the orchestrator may
// already have wrapped it in a vendor-specific JSON shape.
type Message struct {
	ID             string      `json:"id"`
	ConversationID string      `json:"conversation_id"`
	Role           MessageRole `json:"role"`
	Content        string      `json:"content"`
	CreatedAt      time.Time   `json:"created_at"`
}

// FeedbackRating is "good" or "bad". Free-text comments go in
// Comment, but the rating is required.
type FeedbackRating string

const (
	RatingGood FeedbackRating = "good"
	RatingBad  FeedbackRating = "bad"
)

// Feedback is append-only. A second feedback on the same
// message replaces the prior one (last-writer-wins on
// (sub, message_id)). The store keeps only the latest row.
type Feedback struct {
	MessageID string         `json:"message_id"`
	Sub       string         `json:"-"`
	Rating    FeedbackRating `json:"rating"`
	Comment   string         `json:"comment,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Store is the persistence contract. All methods take a context
// and a sub; implementations MUST verify ownership before
// returning rows.
type Store interface {
	CreateConversation(ctx context.Context, sub, title string) (*Conversation, error)
	ListConversations(ctx context.Context, sub string) ([]Conversation, error)
	GetConversation(ctx context.Context, sub, id string) (*Conversation, error)
	RenameConversation(ctx context.Context, sub, id, newTitle string) (*Conversation, error)
	DeleteConversation(ctx context.Context, sub, id string) error

	AppendMessage(ctx context.Context, sub, conversationID string, role MessageRole, content string) (*Message, error)
	ListMessages(ctx context.Context, sub, conversationID string) ([]Message, error)

	UpsertFeedback(ctx context.Context, sub, messageID string, rating FeedbackRating, comment string) (*Feedback, error)
	GetFeedback(ctx context.Context, sub, messageID string) (*Feedback, error)
}

// InMemoryStore is a thread-safe Store backed by maps. Used by
// tests and the dev binary; not durable.
type InMemoryStore struct {
	mu            sync.RWMutex
	conversations map[string]*Conversation         // by id
	messages      map[string][]*Message            // by conversation id
	messageIndex  map[string]string                // message id -> conversation id
	feedback      map[string]map[string]*Feedback  // sub -> message_id -> latest
	now           func() time.Time
	newID         func() string
}

// NewInMemoryStore builds an InMemoryStore using time.Now and
// random UUIDs. Tests can substitute deterministic clocks via
// the exported fields.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		conversations: make(map[string]*Conversation),
		messages:      make(map[string][]*Message),
		messageIndex:  make(map[string]string),
		feedback:      make(map[string]map[string]*Feedback),
		now:           time.Now,
		newID:         func() string { return uuid.NewString() },
	}
}

// SetClock replaces the time source. Test-only.
func (s *InMemoryStore) SetClock(now func() time.Time) { s.now = now }

// SetIDGen replaces the id generator. Test-only.
func (s *InMemoryStore) SetIDGen(gen func() string) { s.newID = gen }

func (s *InMemoryStore) CreateConversation(_ context.Context, sub, title string) (*Conversation, error) {
	if sub == "" {
		return nil, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	c := &Conversation{
		ID:        s.newID(),
		Sub:       sub,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.conversations[c.ID] = c
	return c, nil
}

func (s *InMemoryStore) ListConversations(_ context.Context, sub string) ([]Conversation, error) {
	if sub == "" {
		return nil, ErrForbidden
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Conversation, 0)
	for _, c := range s.conversations {
		if c.Sub == sub {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *InMemoryStore) GetConversation(_ context.Context, sub, id string) (*Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.conversations[id]
	if !ok {
		return nil, ErrNotFound
	}
	if c.Sub != sub {
		// Hide existence cross-tenant.
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (s *InMemoryStore) RenameConversation(_ context.Context, sub, id, newTitle string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[id]
	if !ok || c.Sub != sub {
		return nil, ErrNotFound
	}
	c.Title = newTitle
	c.UpdatedAt = s.now().UTC()
	cp := *c
	return &cp, nil
}

func (s *InMemoryStore) DeleteConversation(_ context.Context, sub, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[id]
	if !ok || c.Sub != sub {
		return ErrNotFound
	}
	delete(s.conversations, id)
	for _, m := range s.messages[id] {
		delete(s.messageIndex, m.ID)
	}
	delete(s.messages, id)
	return nil
}

func (s *InMemoryStore) AppendMessage(_ context.Context, sub, conversationID string, role MessageRole, content string) (*Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[conversationID]
	if !ok || c.Sub != sub {
		return nil, ErrNotFound
	}
	now := s.now().UTC()
	m := &Message{
		ID:             s.newID(),
		ConversationID: conversationID,
		Role:           role,
		Content:        content,
		CreatedAt:      now,
	}
	s.messages[conversationID] = append(s.messages[conversationID], m)
	s.messageIndex[m.ID] = conversationID
	c.UpdatedAt = now
	cp := *m
	return &cp, nil
}

func (s *InMemoryStore) ListMessages(_ context.Context, sub, conversationID string) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.conversations[conversationID]
	if !ok || c.Sub != sub {
		return nil, ErrNotFound
	}
	src := s.messages[conversationID]
	out := make([]Message, len(src))
	for i, m := range src {
		out[i] = *m
	}
	return out, nil
}

func (s *InMemoryStore) UpsertFeedback(_ context.Context, sub, messageID string, rating FeedbackRating, comment string) (*Feedback, error) {
	if rating != RatingGood && rating != RatingBad {
		return nil, errors.New("rating must be good or bad")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	convID, ok := s.messageIndex[messageID]
	if !ok {
		return nil, ErrNotFound
	}
	c := s.conversations[convID]
	if c == nil || c.Sub != sub {
		return nil, ErrNotFound
	}
	f := &Feedback{
		MessageID: messageID,
		Sub:       sub,
		Rating:    rating,
		Comment:   comment,
		CreatedAt: s.now().UTC(),
	}
	if s.feedback[sub] == nil {
		s.feedback[sub] = make(map[string]*Feedback)
	}
	s.feedback[sub][messageID] = f
	cp := *f
	return &cp, nil
}

func (s *InMemoryStore) GetFeedback(_ context.Context, sub, messageID string) (*Feedback, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.feedback[sub] == nil {
		return nil, ErrNotFound
	}
	f, ok := s.feedback[sub][messageID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *f
	return &cp, nil
}
