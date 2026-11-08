package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"leadwatch/internal/config"
	"leadwatch/internal/store"
)

// Notifier dispatches Telegram alerts to the owner.
// It NEVER contacts leads or public sources automatically.
type Notifier struct {
	cfg        *config.Config
	apiURL     string
	httpClient *http.Client
}

func New(cfg *config.Config, httpClient *http.Client) *Notifier {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 20 * time.Second,
		}
	}
	return &Notifier{
		cfg:        cfg,
		apiURL:     "https://api.telegram.org",
		httpClient: httpClient,
	}
}

func (n *Notifier) SetBaseURL(u string) {
	n.apiURL = u
}

type telegramSendMessageReq struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
}

// FormatAlert creates the HTML message for a lead alert.
// All user and external content is strictly HTML escaped.
func FormatAlert(post *store.Post, classification *store.ClassificationRecord, now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	age := now.Sub(post.PublishedAt)
	if age < 0 {
		age = 0
	}
	ageStr := formatDuration(age)

	projectType := "Unknown"
	budgetStr := "Not specified"
	if classification != nil {
		if classification.ProjectType != "" {
			projectType = classification.ProjectType
		}
		if classification.BudgetAmount != nil && *classification.BudgetAmount > 0 {
			curr := "USD"
			if classification.BudgetCurrency != nil && *classification.BudgetCurrency != "" {
				curr = *classification.BudgetCurrency
			}
			budgetStr = fmt.Sprintf("%.0f %s", *classification.BudgetAmount, curr)
		}
	}

	// 2-line summary extracted from post body or title
	summary := extractTwoLineSummary(post.Title, post.Body)

	return fmt.Sprintf(
		"🚨 <b>New Lead (Score: %d/100)</b>\n\n"+
			"<b>Project:</b> %s\n"+
			"<b>Budget:</b> %s\n"+
			"<b>Age:</b> %s\n"+
			"<b>Source:</b> %s\n\n"+
			"<b>Summary:</b>\n%s\n\n"+
			"🔗 <a href=\"%s\">View Lead</a>",
		post.Score,
		html.EscapeString(projectType),
		html.EscapeString(budgetStr),
		html.EscapeString(ageStr),
		html.EscapeString(post.Source),
		html.EscapeString(summary),
		html.EscapeString(post.URL),
	)
}

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		mins := int(d.Minutes())
		if mins < 1 {
			return "just now"
		}
		return fmt.Sprintf("%dm ago", mins)
	}
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := hours / 24
	return fmt.Sprintf("%dd ago", days)
}

func extractTwoLineSummary(title, body string) string {
	line1 := strings.TrimSpace(title)
	if len(line1) > 90 {
		line1 = line1[:87] + "..."
	}

	bodyClean := strings.TrimSpace(body)
	var line2 string
	if bodyClean != "" {
		lines := strings.Split(bodyClean, "\n")
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if trimmed != "" && trimmed != line1 {
				line2 = trimmed
				break
			}
		}
		if line2 == "" {
			line2 = bodyClean
		}
		if len(line2) > 100 {
			line2 = line2[:97] + "..."
		}
	}

	if line2 == "" {
		return line1
	}
	return fmt.Sprintf("%s\n%s", line1, line2)
}

// NotifyAlert sends a formatted HTML alert to the Telegram bot with exponential backoff on 429/5xx.
// Failures log and return an error without blocking or panicking the caller.
func (n *Notifier) NotifyAlert(ctx context.Context, post *store.Post, classification *store.ClassificationRecord) error {
	if err := n.cfg.ValidateTelegram(); err != nil {
		return err
	}

	msgHTML := FormatAlert(post, classification, time.Now().UTC())

	url := fmt.Sprintf("%s/bot%s/sendMessage", n.apiURL, n.cfg.TelegramBotToken)

	payload := telegramSendMessageReq{
		ChatID:    n.cfg.TelegramChatID,
		Text:      msgHTML,
		ParseMode: "HTML",
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	maxRetries := 3
	initialBackoff := 1 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := n.httpClient.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				defer resp.Body.Close()
				var tgResp telegramResponse
				_ = json.NewDecoder(resp.Body).Decode(&tgResp)
				if !tgResp.OK {
					return fmt.Errorf("telegram api error: %s", tgResp.Description)
				}
				return nil
			}

			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				retryAfter := resp.Header.Get("Retry-After")
				resp.Body.Close()

				if attempt == maxRetries {
					return fmt.Errorf("telegram failed with status %d after %d retries", resp.StatusCode, maxRetries)
				}

				var sleepDuration time.Duration
				if sSec, parseErr := strconv.Atoi(retryAfter); parseErr == nil && sSec > 0 {
					sleepDuration = time.Duration(sSec) * time.Second
				} else {
					backoff := float64(initialBackoff) * math.Pow(2, float64(attempt))
					jitter := rand.Float64() * 0.3 * backoff
					sleepDuration = time.Duration(backoff + jitter)
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(sleepDuration):
					continue
				}
			}

			respBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("telegram returned status %d: %s", resp.StatusCode, string(respBytes))
		}

		if attempt == maxRetries {
			return err
		}

		backoff := float64(initialBackoff) * math.Pow(2, float64(attempt))
		jitter := rand.Float64() * 0.3 * backoff
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(backoff + jitter)):
		}
	}

	return errors.New("maximum retries exceeded for telegram alert")
}

// ProcessAlerts processes all eligible drafted/scored leads above minScore, ensuring no duplicate notifications.
func ProcessAlerts(ctx context.Context, st *store.Store, notifier *Notifier, minScore int, limit int) (int, error) {
	// Query posts that are classified as hiring, scored >= minScore, and have NOT been notified yet
	posts, err := st.GetPostsByStatus(ctx, store.StatusDrafted, limit)
	if err != nil {
		return 0, err
	}

	alertedCount := 0
	for _, p := range posts {
		if ctx.Err() != nil {
			return alertedCount, ctx.Err()
		}

		// Never alert the same lead twice
		if p.NotifiedAt != nil {
			continue
		}

		if p.Score < minScore {
			continue
		}

		classRec, err := st.GetDetailedClassification(ctx, p.ID)
		if err != nil {
			slog.Error("failed to retrieve detailed classification for alert", "id", p.ID, "err", err)
		}

		if notifier != nil {
			if err := notifier.NotifyAlert(ctx, p, classRec); err != nil {
				slog.Error("failed to send telegram alert for lead", "id", p.ID, "err", err)
				// Backoff failure without stopping other leads or blocking pipeline
				continue
			}
		}

		now := time.Now().UTC()
		if err := st.MarkNotified(ctx, p.ID, now, store.StatusNotified); err != nil {
			slog.Error("failed to mark lead notified", "id", p.ID, "err", err)
			continue
		}
		alertedCount++
	}

	return alertedCount, nil
}
