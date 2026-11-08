package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leadwatch/internal/config"
	"leadwatch/internal/notify"
	"leadwatch/internal/store"
)

func TestFormatAlert_HTMLEscaping(t *testing.T) {
	now := time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)
	pubTime := now.Add(-45 * time.Minute)

	budget := 5000.0
	currency := "USD"
	classRec := &store.ClassificationRecord{
		ProjectType:    "web_app",
		BudgetAmount:   &budget,
		BudgetCurrency: &currency,
	}

	post := &store.Post{
		Score:       88,
		Source:      "reddit",
		Title:       "<script>alert('xss')</script> Need & Want Developer",
		Body:        "Building an <b>e-commerce</b> platform.\nMust have Go & React skills.",
		URL:         "https://example.com/job?test=1&ref=2",
		PublishedAt: pubTime,
	}

	msg := notify.FormatAlert(post, classRec, now)

	// HTML escaped check
	if strings.Contains(msg, "<script>") {
		t.Errorf("raw <script> tag detected in formatted HTML alert!")
	}
	if !strings.Contains(msg, "&lt;script&gt;") {
		t.Errorf("expected escaped HTML &lt;script&gt;, got: %s", msg)
	}
	if !strings.Contains(msg, "<b>Project:</b> web_app") {
		t.Errorf("expected project type in HTML, got: %s", msg)
	}
	if !strings.Contains(msg, "5000 USD") {
		t.Errorf("expected 5000 USD budget in HTML, got: %s", msg)
	}
	if !strings.Contains(msg, "45m ago") {
		t.Errorf("expected '45m ago' age, got: %s", msg)
	}
}

func TestNotifier_NotifyAlert_Success(t *testing.T) {
	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &receivedBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		TelegramBotToken: "TEST_TOKEN",
		TelegramChatID:   123456789,
	}

	n := notify.New(cfg, server.Client())
	n.SetBaseURL(server.URL)

	post := &store.Post{
		ID:          1,
		Score:       85,
		Source:      "hackernews",
		Title:       "Hiring Senior Go Developer",
		URL:         "https://example.com/job/1",
		PublishedAt: time.Now().UTC().Add(-30 * time.Minute),
	}

	if err := n.NotifyAlert(context.Background(), post, nil, "Hi Alex, here is the draft."); err != nil {
		t.Fatalf("unexpected notify error: %v", err)
	}

	if receivedBody["chat_id"] != float64(123456789) {
		t.Errorf("expected chat_id 123456789, got %v", receivedBody["chat_id"])
	}
	if receivedBody["parse_mode"] != "HTML" {
		t.Errorf("expected parse_mode HTML, got %v", receivedBody["parse_mode"])
	}
}

func TestNotifier_NotifyAlert_RateLimitRetry(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok": false, "description": "Too Many Requests: retry after 1"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		TelegramBotToken: "TEST_TOKEN",
		TelegramChatID:   123456789,
	}

	n := notify.New(cfg, server.Client())
	n.SetBaseURL(server.URL)

	post := &store.Post{
		ID:          1,
		Score:       90,
		Title:       "Urgent Developer",
		PublishedAt: time.Now().UTC(),
	}

	if err := n.NotifyAlert(context.Background(), post, nil, ""); err != nil {
		t.Fatalf("expected successful alert after retry, got: %v", err)
	}

	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestProcessAlerts_NeverAlertTwice(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "notify_alerts.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	var sentAlerts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sentAlerts, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		TelegramBotToken: "TOKEN",
		TelegramChatID:   999,
	}
	notifier := notify.New(cfg, server.Client())
	notifier.SetBaseURL(server.URL)

	ctx := context.Background()

	// Insert lead 1: Score 80 (eligible)
	lead1 := &store.Post{
		Source:      "reddit",
		ExternalID:  "l1",
		Title:       "Hiring developer",
		PublishedAt: time.Now().UTC(),
		Status:      store.StatusDrafted,
	}
	_, _ = st.UpsertRaw(ctx, lead1)
	_ = st.SaveScore(ctx, 1, 80, "match", store.StatusDrafted)

	// Insert lead 2: Score 30 (below minScore 50 floor)
	lead2 := &store.Post{
		Source:      "reddit",
		ExternalID:  "l2",
		Title:       "Low priority",
		PublishedAt: time.Now().UTC(),
		Status:      store.StatusDrafted,
	}
	_, _ = st.UpsertRaw(ctx, lead2)
	_ = st.SaveScore(ctx, 2, 30, "low", store.StatusDrafted)

	// Pass 1: Should alert lead1 only
	count, err := notify.ProcessAlerts(ctx, st, notifier, 50, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 alert sent, got %d", count)
	}
	if atomic.LoadInt32(&sentAlerts) != 1 {
		t.Fatalf("expected 1 HTTP request to Telegram, got %d", atomic.LoadInt32(&sentAlerts))
	}

	// Pass 2: Should NOT alert lead1 again (never alert twice)
	count2, err := notify.ProcessAlerts(ctx, st, notifier, 50, 10)
	if err != nil {
		t.Fatalf("unexpected error on second pass: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 new alerts sent on second pass, got %d", count2)
	}
	if atomic.LoadInt32(&sentAlerts) != 1 {
		t.Fatalf("expected total Telegram requests to remain 1, got %d", atomic.LoadInt32(&sentAlerts))
	}
}
