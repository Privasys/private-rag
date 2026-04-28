// Package store: Postgres-backed implementation of Store.
//
// Schema is in migrations.go; a single CREATE-IF-NOT-EXISTS
// migration runs at startup so deployments don't depend on an
// external migration tool. JWT subject is the unit of ownership;
// every query is scoped by sub and cross-subject access surfaces
// as ErrNotFound.
//
// Connection pooling is delegated to jackc/pgx/v5/pgxpool.

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the production Store implementation.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects to dsn (libpq URL or DSN) and applies
// the bundled schema migration. The returned Store is safe for
// concurrent use.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases pooled connections.
func (s *PostgresStore) Close() { s.pool.Close() }

// Ping returns nil iff the database accepts a SELECT 1.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PostgresStore) CreateConversation(ctx context.Context, sub, title string) (*Conversation, error) {
	if sub == "" {
		return nil, ErrForbidden
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO conversations (id, sub, title, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
	`, id, sub, title, now)
	if err != nil {
		return nil, mapPgErr(err)
	}
	return &Conversation{ID: id, Sub: sub, Title: title, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *PostgresStore) ListConversations(ctx context.Context, sub string) ([]Conversation, error) {
	if sub == "" {
		return nil, ErrForbidden
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, sub, title, created_at, updated_at
		FROM conversations
		WHERE sub = $1
		ORDER BY updated_at DESC
	`, sub)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Sub, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, mapPgErr(err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetConversation(ctx context.Context, sub, id string) (*Conversation, error) {
	var c Conversation
	err := s.pool.QueryRow(ctx, `
		SELECT id, sub, title, created_at, updated_at
		FROM conversations WHERE id = $1 AND sub = $2
	`, id, sub).Scan(&c.ID, &c.Sub, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, mapPgErr(err)
	}
	return &c, nil
}

func (s *PostgresStore) RenameConversation(ctx context.Context, sub, id, title string) (*Conversation, error) {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE conversations SET title = $1, updated_at = $2
		WHERE id = $3 AND sub = $4
	`, title, now, id, sub)
	if err != nil {
		return nil, mapPgErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetConversation(ctx, sub, id)
}

func (s *PostgresStore) DeleteConversation(ctx context.Context, sub, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM conversations WHERE id = $1 AND sub = $2`, id, sub)
	if err != nil {
		return mapPgErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) AppendMessage(ctx context.Context, sub, convID string, role MessageRole, content string) (*Message, error) {
	// Ownership check + update conversation timestamp + insert message
	// in a single tx so we never insert orphan messages.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer tx.Rollback(ctx)

	var owned bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM conversations WHERE id = $1 AND sub = $2)`,
		convID, sub).Scan(&owned); err != nil {
		return nil, mapPgErr(err)
	}
	if !owned {
		return nil, ErrNotFound
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, conversation_id, sub, role, content, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, id, convID, sub, string(role), content, now); err != nil {
		return nil, mapPgErr(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE conversations SET updated_at = $1 WHERE id = $2`, now, convID); err != nil {
		return nil, mapPgErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgErr(err)
	}
	return &Message{
		ID: id, ConversationID: convID,
		Role: role, Content: content, CreatedAt: now,
	}, nil
}

func (s *PostgresStore) ListMessages(ctx context.Context, sub, convID string) ([]Message, error) {
	// Ownership check first so we can return ErrNotFound consistently.
	var owned bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM conversations WHERE id = $1 AND sub = $2)`,
		convID, sub).Scan(&owned); err != nil {
		return nil, mapPgErr(err)
	}
	if !owned {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, conversation_id, role, content, created_at
		FROM messages WHERE conversation_id = $1 AND sub = $2
		ORDER BY created_at ASC
	`, convID, sub)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var role string
		if err := rows.Scan(&m.ID, &m.ConversationID, &role, &m.Content, &m.CreatedAt); err != nil {
			return nil, mapPgErr(err)
		}
		m.Role = MessageRole(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertFeedback(ctx context.Context, sub, msgID string, rating FeedbackRating, comment string) (*Feedback, error) {
	switch rating {
	case RatingGood, RatingBad:
	default:
		return nil, fmt.Errorf("invalid rating %q", rating)
	}
	// Cross-subject check: only allow feedback on messages the caller can see.
	var owned bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM messages WHERE id = $1 AND sub = $2)`,
		msgID, sub).Scan(&owned); err != nil {
		return nil, mapPgErr(err)
	}
	if !owned {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO feedback (message_id, sub, rating, comment, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (message_id, sub) DO UPDATE
		SET rating = EXCLUDED.rating, comment = EXCLUDED.comment, created_at = EXCLUDED.created_at
	`, msgID, sub, string(rating), comment, now)
	if err != nil {
		return nil, mapPgErr(err)
	}
	return &Feedback{
		MessageID: msgID, Sub: sub,
		Rating: rating, Comment: comment, CreatedAt: now,
	}, nil
}

func (s *PostgresStore) GetFeedback(ctx context.Context, sub, msgID string) (*Feedback, error) {
	var f Feedback
	var rating string
	err := s.pool.QueryRow(ctx, `
		SELECT message_id, sub, rating, comment, created_at
		FROM feedback WHERE message_id = $1 AND sub = $2
	`, msgID, sub).Scan(&f.MessageID, &f.Sub, &rating, &f.Comment, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, mapPgErr(err)
	}
	f.Rating = FeedbackRating(rating)
	return &f, nil
}

// mapPgErr converts known Postgres error codes into store sentinels.
// Anything unrecognised is wrapped verbatim.
func mapPgErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "23503": // foreign_key_violation
			return ErrNotFound
		}
	}
	if strings.Contains(err.Error(), "no rows") {
		return ErrNotFound
	}
	return err
}

// migrate creates the schema if missing. Idempotent.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS conversations (
    id          UUID        PRIMARY KEY,
    sub         TEXT        NOT NULL,
    title       TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS conversations_sub_updated_idx
    ON conversations (sub, updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
    id              UUID        PRIMARY KEY,
    conversation_id UUID        NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sub             TEXT        NOT NULL,
    role            TEXT        NOT NULL,
    content         TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_conv_created_idx
    ON messages (conversation_id, created_at);
CREATE INDEX IF NOT EXISTS messages_sub_idx
    ON messages (sub);

CREATE TABLE IF NOT EXISTS feedback (
    message_id  UUID        NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    sub         TEXT        NOT NULL,
    rating      TEXT        NOT NULL,
    comment     TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (message_id, sub)
);

-- pgvector tables are reserved for the upcoming Tools data plane.
-- The extension is optional today; create only if available so the
-- bare 'postgres' image still works for a conversations-only deploy.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
        CREATE EXTENSION IF NOT EXISTS vector;
        CREATE TABLE IF NOT EXISTS rag_documents (
            id              UUID        PRIMARY KEY,
            sub             TEXT        NOT NULL,
            source_blob_uri TEXT        NOT NULL,
            title           TEXT        NOT NULL DEFAULT '',
            mime_type       TEXT        NOT NULL DEFAULT '',
            status          TEXT        NOT NULL DEFAULT 'queued',
            created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
            UNIQUE (sub, source_blob_uri)
        );
        CREATE TABLE IF NOT EXISTS rag_chunks (
            id           UUID        PRIMARY KEY,
            document_id  UUID        NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
            sub          TEXT        NOT NULL,
            ord          INTEGER     NOT NULL,
            text         TEXT        NOT NULL,
            source_offset BIGINT     NOT NULL DEFAULT 0,
            embedding    vector(384)
        );
        CREATE INDEX IF NOT EXISTS rag_chunks_sub_idx ON rag_chunks (sub);
    END IF;
END
$$;
`
