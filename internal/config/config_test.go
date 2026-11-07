package config_test

import (
	"os"
	"testing"

	"leadwatch/internal/config"
)

func TestLoadFromEnv(t *testing.T) {
	os.Unsetenv("DB_PATH")
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Setenv("LOG_LEVEL", "DEBUG")
	os.Setenv("REDDIT_CLIENT_ID", "rc_id_123")
	os.Setenv("REDDIT_CLIENT_SECRET", "rc_secret_456")
	os.Setenv("TELEGRAM_BOT_TOKEN", "tg_token_789")
	os.Setenv("TELEGRAM_CHAT_ID", "99887766")

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DBPath != "leadwatch.db" {
		t.Errorf("expected default db path leadwatch.db, got %s", cfg.DBPath)
	}
	if cfg.ClassifierModel == "" || cfg.DrafterModel == "" {
		t.Error("expected default models to be populated")
	}

	if err := cfg.ValidateReddit(); err != nil {
		t.Errorf("expected valid reddit credentials, got: %v", err)
	}

	if err := cfg.ValidateTelegram(); err != nil {
		t.Errorf("expected valid telegram credentials, got: %v", err)
	}

	if err := cfg.ValidateAnthropic(); err == nil {
		t.Error("expected error when ANTHROPIC_API_KEY is missing, got nil")
	}
}
