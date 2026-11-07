package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"leadwatch/internal/config"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

type Notifier struct {
	cfg    config.TelegramConfig
	client *source.Client
	apiURL string
}

func New(cfg config.TelegramConfig, httpClient *http.Client) *Notifier {
	return &Notifier{
		cfg:    cfg,
		client: source.NewClient(httpClient),
		apiURL: "https://api.telegram.org",
	}
}

func (n *Notifier) SetBaseURL(u string) {
	n.apiURL = u
}

type telegramSendMessageReq struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// NotifyAlert sends a high-priority alert to the owner's Telegram chat.
// Reminder: This ONLY alerts the owner on Telegram about the lead and draft.
// It NEVER sends messages automatically to leads or public sources.
func (n *Notifier) NotifyAlert(ctx context.Context, p *store.Post) error {
	if !n.cfg.Enabled || n.cfg.BotToken == "" || n.cfg.ChatID == 0 {
		return nil
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", n.apiURL, n.cfg.BotToken)

	msgText := fmt.Sprintf(
		"🚨 *New High-Scoring Web Dev Lead* (Score: %d/100)\n\n"+
			"*Source:* %s\n"+
			"*Title:* %s\n"+
			"*Author:* %s\n"+
			"*URL:* %s\n\n"+
			"*Match Reason:* %s\n\n"+
			"📝 *Drafted Reply (For Manual Review):*\n```\n%s\n```",
		p.Score,
		p.Source,
		escapeMarkdown(p.Title),
		escapeMarkdown(p.Author),
		p.URL,
		escapeMarkdown(p.ScoreReason),
		p.DraftReply,
	)

	payload := telegramSendMessageReq{
		ChatID:    n.cfg.ChatID,
		Text:      msgText,
		ParseMode: "Markdown",
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram notification failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned status %d", resp.StatusCode)
	}

	return nil
}

func escapeMarkdown(s string) string {
	// Simple escape for Telegram Markdown mode
	var out []rune
	for _, r := range s {
		switch r {
		case '_', '*', '`', '[':
			out = append(out, '\\', r)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
