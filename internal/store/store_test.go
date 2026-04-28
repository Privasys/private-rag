package store

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *InMemoryStore {
	t.Helper()
	s := NewInMemoryStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	s.SetClock(func() time.Time {
		tick++
		return now.Add(time.Duration(tick) * time.Second)
	})
	id := 0
	s.SetIDGen(func() string {
		id++
		return idForTest(id)
	})
	return s
}

func idForTest(n int) string {
	const hex = "0123456789abcdef"
	out := []byte("00000000-0000-0000-0000-000000000000")
	i := len(out) - 1
	for n > 0 && i >= 0 {
		if out[i] == '-' {
			i--
			continue
		}
		out[i] = hex[n%16]
		n /= 16
		i--
	}
	return string(out)
}

func TestConversationCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	c, err := s.CreateConversation(ctx, "user-a", "first")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Sub != "user-a" || c.Title != "first" {
		t.Fatalf("unexpected conversation: %+v", c)
	}

	got, err := s.GetConversation(ctx, "user-a", c.ID)
	if err != nil || got.ID != c.ID {
		t.Fatalf("get: %v %+v", err, got)
	}

	// Cross-tenant access must look like a 404, not a 403.
	if _, err := s.GetConversation(ctx, "user-b", c.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if _, err := s.RenameConversation(ctx, "user-b", c.ID, "stolen"); err != ErrNotFound {
		t.Fatalf("rename cross-tenant: %v", err)
	}
	renamed, err := s.RenameConversation(ctx, "user-a", c.ID, "renamed")
	if err != nil || renamed.Title != "renamed" {
		t.Fatalf("rename: %v %+v", err, renamed)
	}

	if err := s.DeleteConversation(ctx, "user-b", c.ID); err != ErrNotFound {
		t.Fatalf("delete cross-tenant: %v", err)
	}
	if err := s.DeleteConversation(ctx, "user-a", c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetConversation(ctx, "user-a", c.ID); err != ErrNotFound {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestListConversationsOrderedByUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	a, _ := s.CreateConversation(ctx, "u", "a")
	b, _ := s.CreateConversation(ctx, "u", "b")
	// Touch a after b so it bubbles to the top.
	if _, err := s.RenameConversation(ctx, "u", a.ID, "a-touched"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	list, err := s.ListConversations(ctx, "u")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d", len(list))
	}
	if list[0].ID != a.ID {
		t.Fatalf("ordering wrong: %+v", list)
	}
	_ = b
}

func TestMessagesAndFeedback(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	c, _ := s.CreateConversation(ctx, "u", "chat")
	user, err := s.AppendMessage(ctx, "u", c.ID, RoleUser, "hello")
	if err != nil {
		t.Fatalf("append user: %v", err)
	}
	asst, err := s.AppendMessage(ctx, "u", c.ID, RoleAssistant, "hi")
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	msgs, err := s.ListMessages(ctx, "u", c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("list messages: %v %d", err, len(msgs))
	}
	if msgs[0].ID != user.ID || msgs[1].ID != asst.ID {
		t.Fatalf("wrong order: %+v", msgs)
	}

	if _, err := s.AppendMessage(ctx, "intruder", c.ID, RoleUser, "x"); err != ErrNotFound {
		t.Fatalf("cross-tenant append: %v", err)
	}

	fb, err := s.UpsertFeedback(ctx, "u", asst.ID, RatingBad, "wrong answer")
	if err != nil || fb.Rating != RatingBad {
		t.Fatalf("feedback: %v %+v", err, fb)
	}
	// Last-writer-wins.
	if _, err := s.UpsertFeedback(ctx, "u", asst.ID, RatingGood, ""); err != nil {
		t.Fatalf("feedback upsert: %v", err)
	}
	got, err := s.GetFeedback(ctx, "u", asst.ID)
	if err != nil || got.Rating != RatingGood || got.Comment != "" {
		t.Fatalf("feedback after upsert: %v %+v", err, got)
	}

	// Cross-tenant feedback on someone else's message is invisible.
	if _, err := s.UpsertFeedback(ctx, "intruder", asst.ID, RatingGood, ""); err != ErrNotFound {
		t.Fatalf("cross-tenant feedback: %v", err)
	}
	if _, err := s.GetFeedback(ctx, "intruder", asst.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant get feedback: %v", err)
	}

	// Bad rating value rejected.
	if _, err := s.UpsertFeedback(ctx, "u", asst.ID, FeedbackRating("meh"), ""); err == nil {
		t.Fatalf("expected error for bad rating")
	}
}

func TestBranchFromMessage(t *testing.T) {
ctx := context.Background()
s := newTestStore(t)

c, _ := s.CreateConversation(ctx, "u", "root")
m1, _ := s.AppendMessage(ctx, "u", c.ID, RoleUser, "q1")
m2, _ := s.AppendMessage(ctx, "u", c.ID, RoleAssistant, "a1")
m3, _ := s.AppendMessage(ctx, "u", c.ID, RoleUser, "q2")
_, _ = s.AppendMessage(ctx, "u", c.ID, RoleAssistant, "a2")

// Branch from m2: new conversation should contain m1 + m2 only.
b, err := s.BranchFromMessage(ctx, "u", m2.ID, "")
if err != nil {
t.Fatalf("branch: %v", err)
}
if b.ID == c.ID {
t.Fatalf("branch must mint new id")
}
if b.Title != "root (branch)" {
t.Fatalf("default title = %q", b.Title)
}
bm, err := s.ListMessages(ctx, "u", b.ID)
if err != nil || len(bm) != 2 {
t.Fatalf("branch messages: %v len=%d", err, len(bm))
}
if bm[0].Content != "q1" || bm[1].Content != "a1" {
t.Fatalf("wrong slice: %+v", bm)
}
if bm[0].ID == m1.ID || bm[1].ID == m2.ID {
t.Fatalf("branch must reissue message ids")
}

// Source conversation untouched.
src, _ := s.ListMessages(ctx, "u", c.ID)
if len(src) != 4 {
t.Fatalf("source mutated: len=%d", len(src))
}

// Branching from the last message includes everything.
full, err := s.BranchFromMessage(ctx, "u", m3.ID, "fork-q2")
if err != nil {
t.Fatalf("branch full: %v", err)
}
if full.Title != "fork-q2" {
t.Fatalf("title override: %q", full.Title)
}
fm, _ := s.ListMessages(ctx, "u", full.ID)
if len(fm) != 3 {
t.Fatalf("expected 3 messages, got %d", len(fm))
}

// Cross-tenant attempt hides the row.
if _, err := s.BranchFromMessage(ctx, "intruder", m2.ID, ""); err != ErrNotFound {
t.Fatalf("cross-tenant branch: %v", err)
}
// Empty subject rejected.
if _, err := s.BranchFromMessage(ctx, "", m2.ID, ""); err != ErrForbidden {
t.Fatalf("empty sub: %v", err)
}
// Unknown message is ErrNotFound.
if _, err := s.BranchFromMessage(ctx, "u", "no-such-id", ""); err != ErrNotFound {
t.Fatalf("unknown msg: %v", err)
}
}
