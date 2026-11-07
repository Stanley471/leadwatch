package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config holds user-configurable options for leadwatch.
type Config struct {
	DatabasePath string         `json:"database_path"`
	PollInterval time.Duration  `json:"poll_interval"`
	Telegram     TelegramConfig `json:"telegram"`
	Sources      SourcesConfig  `json:"sources"`
	Developer    DeveloperBio   `json:"developer"`
}

type TelegramConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   int64  `json:"chat_id"`
}

type SourcesConfig struct {
	HackerNewsJobs bool `json:"hn_jobs"`
	RemoteOK       bool `json:"remoteok"`
	WWR            bool `json:"weworkremotely"`
}

type DeveloperBio struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Skills      []string `json:"skills"`
	Portfolio   string   `json:"portfolio"`
	MinScore    int      `json:"min_score"`
}

// DefaultConfig returns safe defaults.
func DefaultConfig() *Config {
	return &Config{
		DatabasePath: "leadwatch.db",
		PollInterval: 15 * time.Minute,
		Telegram: TelegramConfig{
			Enabled: false,
		},
		Sources: SourcesConfig{
			HackerNewsJobs: true,
			RemoteOK:       true,
			WWR:            true,
		},
		Developer: DeveloperBio{
			Name:      "Leadwatch User",
			Title:     "Full-stack Web Developer",
			Skills:    []string{"Go", "React", "TypeScript", "Node.js", "PostgreSQL", "Next.js", "TailwindCSS"},
			Portfolio: "https://example.com",
			MinScore:  50,
		},
	}
}

// LoadConfig loads configuration from a JSON file, or environment variables if file is absent.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("invalid config json: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	// Environment variable overrides
	if dbPath := os.Getenv("LEADWATCH_DB"); dbPath != "" {
		cfg.DatabasePath = dbPath
	}
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		cfg.Telegram.BotToken = token
		cfg.Telegram.Enabled = true
	}
	if cid := os.Getenv("TELEGRAM_CHAT_ID"); cid != "" {
		var chatID int64
		if _, err := fmt.Sscanf(cid, "%d", &chatID); err == nil {
			cfg.Telegram.ChatID = chatID
		}
	}

	if cfg.DatabasePath == "" {
		cfg.DatabasePath = filepath.Join(".", "leadwatch.db")
	}

	return cfg, nil
}
