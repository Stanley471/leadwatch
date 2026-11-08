package classify_test

import (
	"context"
	_ "embed"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"leadwatch/internal/classify"
	"leadwatch/internal/store"
)

//go:embed testdata/anthropic_valid.json
var anthropicValidFixture []byte

//go:embed testdata/anthropic_scam.json
var anthropicScamFixture []byte

//go:embed testdata/anthropic_malformed.json
var anthropicMalformedFixture []byte

func TestAnthropicClassifier_Classify_ValidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Errorf("expected x-api-key 'test-api-key', got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version '2023-06-01', got %s", r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(anthropicValidFixture)
	}))
	defer server.Close()

	c := classify.NewAnthropicClassifier("test-api-key", "claude-3-5-haiku-20241022", server.Client())
	c.SetBaseURL(server.URL)

	post := &store.Post{
		Source: "reddit",
		Author: "client123",
		Title:  "[Hiring] Need Go & React Developer for SaaS",
		Body:   "We are looking for a contractor to build our web app. Budget: $4500 USD.",
	}

	res, err := c.Classify(context.Background(), post)
	if err != nil {
		t.Fatalf("unexpected classify error: %v", err)
	}

	if !res.IsRequest {
		t.Errorf("expected is_request=true, got %v", res.IsRequest)
	}
	if res.ProjectType != "web_app" {
		t.Errorf("expected project_type web_app, got %s", res.ProjectType)
	}
	if res.BudgetAmount == nil || *res.BudgetAmount != 4500 {
		t.Errorf("expected budget_amount 4500, got %v", res.BudgetAmount)
	}
	if res.BudgetCurrency == nil || *res.BudgetCurrency != "USD" {
		t.Errorf("expected budget_currency USD, got %v", res.BudgetCurrency)
	}
	if res.Urgency != "high" {
		t.Errorf("expected urgency high, got %s", res.Urgency)
	}
	if res.ScamScore != 5 {
		t.Errorf("expected scam_score 5, got %d", res.ScamScore)
	}
	if res.Confidence != 95 {
		t.Errorf("expected confidence 95, got %d", res.Confidence)
	}
}

func TestAnthropicClassifier_Classify_ScamDetection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(anthropicScamFixture)
	}))
	defer server.Close()

	c := classify.NewAnthropicClassifier("test-api-key", "claude-3-5-haiku-20241022", server.Client())
	c.SetBaseURL(server.URL)

	post := &store.Post{
		Source: "forhire",
		Author: "shady_user",
		Title:  "Need simple website for $20",
		Body:   "DM me on Telegram immediately to get started.",
	}

	res, err := c.Classify(context.Background(), post)
	if err != nil {
		t.Fatalf("unexpected classify error: %v", err)
	}

	if res.ScamScore < 70 {
		t.Errorf("expected high scam_score (>70), got %d", res.ScamScore)
	}
}

func TestAnthropicClassifier_RetryOnInvalidJSON(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			// First call: return malformed JSON
			w.Write(anthropicMalformedFixture)
			return
		}
		// Second call: return valid JSON
		w.Write(anthropicValidFixture)
	}))
	defer server.Close()

	c := classify.NewAnthropicClassifier("test-api-key", "claude-3-5-haiku-20241022", server.Client())
	c.SetBaseURL(server.URL)

	post := &store.Post{
		Title: "Need Go Engineer",
		Body:  "Looking for experienced engineer",
	}

	res, err := c.Classify(context.Background(), post)
	if err != nil {
		t.Fatalf("expected successful recovery after retry, got: %v", err)
	}

	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts (initial failure + retry), got %d", atomic.LoadInt32(&attempts))
	}
	if !res.IsRequest {
		t.Errorf("expected is_request=true, got %v", res.IsRequest)
	}
}

func TestAnthropicClassifier_RateLimitRetry(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count == 1 {
			w.Header().Set("retry-after", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(anthropicValidFixture)
	}))
	defer server.Close()

	c := classify.NewAnthropicClassifier("test-api-key", "claude-3-5-haiku-20241022", server.Client())
	c.SetBaseURL(server.URL)

	post := &store.Post{
		Title: "Urgent hire for React developer",
	}

	res, err := c.Classify(context.Background(), post)
	if err != nil {
		t.Fatalf("expected successful classification after rate limit backoff, got: %v", err)
	}

	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
	}
	if !res.IsRequest {
		t.Errorf("expected valid classification result, got %v", res.IsRequest)
	}
}

func TestProcessToClassifyBatch_StageTransition(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "classify_batch.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Insert post with status "to_classify"
	p := &store.Post{
		Source:      "reddit",
		ExternalID:  "item_tc_1",
		URL:         "https://example.com/tc1",
		Title:       "Need a web app",
		Author:      "founder",
		Body:        "Looking for fullstack developer",
		PublishedAt: time.Now().UTC(),
		Status:      store.StatusToClassify,
	}
	_, err = st.UpsertRaw(ctx, p)
	if err != nil {
		t.Fatalf("failed to insert raw: %v", err)
	}

	// Fake classifier
	budget := float64(5000)
	currency := "USD"
	remote := true
	fake := &classify.FakeClassifier{
		Result: classify.ClassificationResult{
			IsRequest:      true,
			ProjectType:    "web_app",
			BudgetAmount:   &budget,
			BudgetCurrency: &currency,
			Urgency:        "high",
			RemoteOK:       &remote,
			Tech:           []string{"Go", "React"},
			ScamScore:      0,
			Confidence:     90,
			Reason:         "Clear validated business project",
		},
	}

	count, err := classify.ProcessToClassifyBatch(ctx, st, fake, 10)
	if err != nil {
		t.Fatalf("ProcessToClassifyBatch failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 post classified, got %d", count)
	}

	// Verify post transitioned to "classified"
	classifiedPosts, err := st.GetPostsByStatus(ctx, store.StatusClassified, 10)
	if err != nil || len(classifiedPosts) != 1 {
		t.Fatalf("expected 1 post in status classified, got %d, err=%v", len(classifiedPosts), err)
	}

	// Verify classification record was saved in classifications table
	rec, err := st.GetDetailedClassification(ctx, classifiedPosts[0].ID)
	if err != nil || rec == nil {
		t.Fatalf("expected classification record, got %v, err=%v", rec, err)
	}
	if rec.ProjectType != "web_app" {
		t.Errorf("expected project_type web_app, got %s", rec.ProjectType)
	}
	if rec.Confidence != 90 {
		t.Errorf("expected confidence 90, got %d", rec.Confidence)
	}
	if rec.Tech != "Go,React" {
		t.Errorf("expected tech Go,React, got %s", rec.Tech)
	}
}
