package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"leadwatch/internal/config"
	"leadwatch/internal/notify"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

func TestNotifier(t *testing.T) {
	var receivedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != source.DefaultUserAgent {
			t.Errorf("expected user agent %s, got %s", source.DefaultUserAgent, r.Header.Get("User-Agent"))
		}
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
		Author:      "TechFounder",
		URL:         "https://example.com/job/1",
		ScoreReason: "skills_matched:2",
		DraftReply:  "Hi TechFounder,\nI'd love to help.",
	}

	if err := n.NotifyAlert(context.Background(), post); err != nil {
		t.Fatalf("unexpected notify error: %v", err)
	}

	if receivedBody["chat_id"] != float64(123456789) {
		t.Errorf("expected chat_id 123456789, got %v", receivedBody["chat_id"])
	}
}
