package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"leadwatch/internal/store"
)

func TestStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	post := &store.Post{
		Source:      "hackernews",
		ExternalID:  "item_1",
		URL:         "https://example.com/item_1",
		Title:       "Test Post",
		Author:      "test_user",
		Body:        "Looking for developer",
		PublishedAt: time.Now().UTC(),
	}

	// 1. Insert Raw
	ok, err := st.UpsertRaw(ctx, post)
	if err != nil || !ok {
		t.Fatalf("expected successful raw insertion, ok=%v, err=%v", ok, err)
	}

	// Deduplication test (should return false and not error)
	ok, err = st.UpsertRaw(ctx, post)
	if err != nil || ok {
		t.Fatalf("expected duplicate upsert to return false, ok=%v, err=%v", ok, err)
	}

	// 2. Query Raw
	posts, err := st.GetPostsByStatus(ctx, store.StatusRaw, 10)
	if err != nil || len(posts) != 1 {
		t.Fatalf("expected 1 raw post, got %d, err=%v", len(posts), err)
	}
	p := posts[0]

	// 3. Save Classification
	if err := st.SaveClassification(ctx, p.ID, true, "web_development", 0.9, store.StatusClassified); err != nil {
		t.Fatalf("failed to save classification: %v", err)
	}

	// 4. Save Score
	if err := st.SaveScore(ctx, p.ID, 85, "match", store.StatusScored); err != nil {
		t.Fatalf("failed to save score: %v", err)
	}

	// 5. Save Draft
	if err := st.SaveDraft(ctx, p.ID, "Hi, I can help", time.Now(), store.StatusDrafted); err != nil {
		t.Fatalf("failed to save draft: %v", err)
	}

	// 6. Mark Notified
	if err := st.MarkNotified(ctx, p.ID, time.Now(), store.StatusNotified); err != nil {
		t.Fatalf("failed to mark notified: %v", err)
	}

	// 7. Verify in list
	leads, err := st.ListLatestLeads(ctx, 50, 10)
	if err != nil || len(leads) != 1 {
		t.Fatalf("expected 1 lead in list, got %d, err=%v", len(leads), err)
	}
	if leads[0].DraftReply != "Hi, I can help" {
		t.Errorf("expected draft reply to match, got %s", leads[0].DraftReply)
	}

	_ = os.Remove(dbPath)
}
