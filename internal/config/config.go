package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds environment-driven configuration for leadwatch.
type Config struct {
	DBPath             string        `json:"db_path"`
	LogLevel           slog.Level    `json:"log_level"`
	RedditClientID     string        `json:"reddit_client_id"`
	RedditClientSecret string        `json:"reddit_client_secret"`
	TelegramBotToken   string        `json:"telegram_bot_token"`
	TelegramChatID     int64         `json:"telegram_chat_id"`
	AnthropicAPIKey    string        `json:"anthropic_api_key"`
	ClassifierModel    string        `json:"classifier_model"`
	DrafterModel       string        `json:"drafter_model"`
	PollInterval       time.Duration `json:"poll_interval"`
}

// LoadFromEnv reads configuration from environment variables with defaults.
// Only DB_PATH is required at startup (defaults to ./leadwatch.db).
func LoadFromEnv() (*Config, error) {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(".", "leadwatch.db")
	}

	logLevelStr := strings.ToUpper(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	var logLevel slog.Level
	switch logLevelStr {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "WARN", "WARNING":
		logLevel = slog.LevelWarn
	case "ERROR":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	classifierModel := os.Getenv("CLASSIFIER_MODEL")
	if classifierModel == "" {
		classifierModel = "claude-3-5-haiku-20241022"
	}

	drafterModel := os.Getenv("DRAFTER_MODEL")
	if drafterModel == "" {
		drafterModel = "claude-3-5-sonnet-20241022"
	}

	var telegramChatID int64
	if cidStr := os.Getenv("TELEGRAM_CHAT_ID"); cidStr != "" {
		if parsed, err := strconv.ParseInt(cidStr, 10, 64); err == nil {
			telegramChatID = parsed
		}
	}

	pollInterval := 15 * time.Minute
	if piStr := os.Getenv("POLL_INTERVAL"); piStr != "" {
		if d, err := time.ParseDuration(piStr); err == nil && d >= time.Minute {
			pollInterval = d
		}
	}

	cfg := &Config{
		DBPath:             dbPath,
		LogLevel:           logLevel,
		RedditClientID:     os.Getenv("REDDIT_CLIENT_ID"),
		RedditClientSecret: os.Getenv("REDDIT_CLIENT_SECRET"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:     telegramChatID,
		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		ClassifierModel:    classifierModel,
		DrafterModel:       drafterModel,
		PollInterval:       pollInterval,
	}

	return cfg, nil
}

// ValidateTelegram ensures Telegram credentials are set before notifying.
func (c *Config) ValidateTelegram() error {
	if c.TelegramBotToken == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is required for Telegram notifications")
	}
	if c.TelegramChatID == 0 {
		return errors.New("TELEGRAM_CHAT_ID is required for Telegram notifications")
	}
	return nil
}

// ValidateReddit ensures Reddit API credentials are set before Reddit polling.
func (c *Config) ValidateReddit() error {
	if c.RedditClientID == "" {
		return errors.New("REDDIT_CLIENT_ID is required for Reddit polling")
	}
	if c.RedditClientSecret == "" {
		return errors.New("REDDIT_CLIENT_SECRET is required for Reddit polling")
	}
	return nil
}

// ValidateAnthropic ensures Anthropic API credentials are configured before LLM stages.
func (c *Config) ValidateAnthropic() error {
	if c.AnthropicAPIKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is required for classification model '%s' and drafting model '%s'", c.ClassifierModel, c.DrafterModel)
	}
	return nil
}
