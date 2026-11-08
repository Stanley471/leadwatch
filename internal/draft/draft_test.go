package draft_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"leadwatch/internal/draft"
	"leadwatch/internal/store"
)

func TestFindBestPortfolioMatch(t *testing.T) {
	items := []store.PortfolioItem{
		{
			Title:       "ShopFlow E-commerce",
			URL:         "https://example.com/shopflow",
			Description: "Online storefront with Stripe and cart checkout",
			Tags:        "ecommerce,shopify,stripe,cart",
		},
		{
			Title:       "CloudSaaS Analytics",
			URL:         "https://example.com/saas",
			Description: "Dashboard and analytics portal",
			Tags:        "web_app,go,react,analytics",
		},
	}

	post := &store.Post{
		Title: "Need developer to build e-commerce shop with Stripe",
		Body:  "Looking for Shopify and cart integration experience.",
	}
	classRec := &store.ClassificationRecord{
		ProjectType: "ecommerce",
		Tech:        "Stripe,Shopify",
	}

	best := draft.FindBestPortfolioMatch(items, post, classRec)
	if best == nil {
		t.Fatal("expected portfolio match, got nil")
	}
	if best.Title != "ShopFlow E-commerce" {
		t.Errorf("expected ShopFlow E-commerce match, got %s", best.Title)
	}
}

func TestGuessChannel(t *testing.T) {
	tests := []struct {
		source   string
		body     string
		expected string
	}{
		{source: "reddit", body: "Please email me at contact@example.com", expected: "email"},
		{source: "reddit", body: "Send me a chat request", expected: "reddit_dm"},
		{source: "reddit", body: "Send me a PM with portfolio", expected: "reddit_pm"},
		{source: "hackernews_freelancer", body: "email: alex@corp.com", expected: "email"},
		{source: "remoteok", body: "Apply via link", expected: "application_url"},
	}

	for _, tt := range tests {
		post := &store.Post{Source: tt.source, Body: tt.body}
		channel := draft.GuessChannel(post)
		if channel != tt.expected {
			t.Errorf("for source=%s, expected %s, got %s", tt.source, tt.expected, channel)
		}
	}
}

func TestGenerateDraft_AnthropicAPI(t *testing.T) {
	mockResponse := `Hi Alex, I saw your post looking for a web app developer and would love to build this with you. I recently completed CloudSaaS (https://example.com/saas), which involved a similar PostgreSQL and Go stack. I recommend using a lightweight REST architecture to keep response times under 50ms and simplify frontend integration. Are the database schemas already drafted, or would you like me to design the data models as well? Would you be available for a 10-minute introductory call tomorrow to discuss timelines?`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]string{
				{"type": "text", "text": mockResponse},
			},
		})
	}))
	defer server.Close()

	drafter := draft.New("test_key", "claude-3-5-sonnet-20241022", server.Client())
	drafter.SetBaseURL(server.URL)

	post := &store.Post{
		Title:  "Need Go developer",
		Author: "Alex",
		Body:   "Looking to build a SaaS portal.",
	}
	portfolio := &store.PortfolioItem{
		Title: "CloudSaaS",
		URL:   "https://example.com/saas",
	}

	reply, err := drafter.GenerateDraft(context.Background(), post, nil, nil, portfolio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(reply, "CloudSaaS") {
		t.Errorf("expected draft to reference real portfolio project CloudSaaS, got: %s", reply)
	}
	if !strings.Contains(reply, "https://example.com/saas") {
		t.Errorf("expected draft to reference portfolio URL, got: %s", reply)
	}
}

func TestProcessDraftingBatch_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "drafting_test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// 1. Add portfolio item
	_ = st.AddPortfolioItem(ctx, store.PortfolioItem{
		Title:       "FastStore",
		URL:         "https://example.com/faststore",
		Description: "E-commerce platform",
		Tags:        "ecommerce,react",
	})

	// 2. Insert scored post
	post := &store.Post{
		Source:      "reddit",
		ExternalID:  "p_score_1",
		Title:       "Need ecommerce developer",
		Author:      "Sam",
		Body:        "Looking for React developer. Send PM.",
		PublishedAt: time.Now().UTC(),
		Status:      store.StatusScored,
	}
	_, _ = st.UpsertRaw(ctx, post)
	_ = st.SaveScore(ctx, 1, 85, "match", store.StatusScored)

	drafter := draft.New("", "", nil) // uses template mode
	count, err := draft.ProcessDraftingBatch(ctx, st, drafter, 10)
	if err != nil {
		t.Fatalf("process batch failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 drafted lead, got %d", count)
	}

	// 3. Verify outreach table entry
	outreach, err := st.GetOutreach(ctx, 1)
	if err != nil || outreach == nil {
		t.Fatalf("expected outreach record, got %v, err=%v", outreach, err)
	}
	if outreach.Channel != "reddit_pm" {
		t.Errorf("expected channel reddit_pm, got %s", outreach.Channel)
	}
	if outreach.Status != "drafted" {
		t.Errorf("expected status drafted, got %s", outreach.Status)
	}
	if !strings.Contains(outreach.DraftReply, "FastStore") {
		t.Errorf("expected draft to reference FastStore portfolio, got: %s", outreach.DraftReply)
	}
}
