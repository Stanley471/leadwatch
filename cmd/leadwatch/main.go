package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"leadwatch/internal/config"
	"leadwatch/internal/notify"
	"leadwatch/internal/pipeline"
	"leadwatch/internal/scheduler"
	"leadwatch/internal/score"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Slog JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch subcommand {
	case "migrate":
		runMigrate(ctx, cfg, os.Args[2:])
	case "poll":
		runPoll(ctx, cfg, os.Args[2:])
	case "run":
		runDaemon(ctx, cfg, os.Args[2:])
	case "leads":
		runLeads(ctx, cfg, os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		slog.Error("unknown subcommand", "subcommand", subcommand)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`leadwatch - monitor public sources for web developer hiring opportunities

Usage:
  leadwatch <subcommand> [flags]

Subcommands:
  migrate    Initialize or apply migrations to the SQLite database
  poll       Run a single ingestion and pipeline cycle then exit
  run        Start leadwatch background daemon loop
  leads      List discovered high-scoring leads

Run 'leadwatch <subcommand> -h' for subcommand options.`)
}

func runMigrate(ctx context.Context, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbPath := fs.String("db", cfg.DBPath, "path to sqlite database")
	_ = fs.Parse(args)

	slog.Info("running sqlite database migration", "db_path", *dbPath)
	st, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	slog.Info("database migration completed successfully", "db_path", *dbPath)
}

func runPoll(ctx context.Context, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("poll", flag.ExitOnError)
	dbPath := fs.String("db", cfg.DBPath, "path to sqlite database")
	sourceFlag := fs.String("source", "all", "source to poll: reddit, hn, or all")
	_ = fs.Parse(args)

	slog.Info("running single poll cycle", "db_path", *dbPath, "source", *sourceFlag)
	st, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	client := source.NewClient(nil)
	sources := []source.Source{
		source.NewHNSource(client),
		source.NewHNFreelancerSource(client),
		source.NewRemoteOKSource(client),
		source.NewWWRSource(client),
		source.NewRedditSource(client, source.RedditConfig{
			ClientID:     cfg.RedditClientID,
			ClientSecret: cfg.RedditClientSecret,
		}),
	}

	bio := score.DefaultBio()
	notifier := notify.New(cfg, nil)
	pipe := pipeline.New(cfg, st, sources, bio, notifier)

	if err := pipe.RunOnce(ctx, *sourceFlag); err != nil {
		slog.Error("poll RunOnce failed", "err", err)
		os.Exit(1)
	}
	slog.Info("single poll cycle finished")
}

func runDaemon(ctx context.Context, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dbPath := fs.String("db", cfg.DBPath, "path to sqlite database")
	_ = fs.Parse(args)

	slog.Info("starting leadwatch daemon", "db_path", *dbPath, "poll_interval", cfg.PollInterval.String())
	st, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	client := source.NewClient(nil)
	sources := []source.Source{
		source.NewHNSource(client),
		source.NewHNFreelancerSource(client),
		source.NewRemoteOKSource(client),
		source.NewWWRSource(client),
		source.NewRedditSource(client, source.RedditConfig{
			ClientID:     cfg.RedditClientID,
			ClientSecret: cfg.RedditClientSecret,
		}),
	}

	bio := score.DefaultBio()
	notifier := notify.New(cfg, nil)
	pipe := pipeline.New(cfg, st, sources, bio, notifier)

	sched := scheduler.New(pipe, sources, cfg.PollInterval)
	if err := sched.Start(ctx); err != nil && err != context.Canceled {
		slog.Error("daemon terminated with error", "err", err)
		os.Exit(1)
	}
	slog.Info("daemon shutdown gracefully")
}

func runLeads(ctx context.Context, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("leads", flag.ExitOnError)
	dbPath := fs.String("db", cfg.DBPath, "path to sqlite database")
	minScore := fs.Int("min-score", 50, "minimum score threshold")
	limit := fs.Int("limit", 50, "maximum leads to return")
	_ = fs.Parse(args)

	st, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	leads, err := st.ListLatestLeads(ctx, *minScore, *limit)
	if err != nil {
		slog.Error("failed to query leads", "err", err)
		os.Exit(1)
	}

	fmt.Printf("=== DISCOVERED LEADS (Min Score: %d, Found: %d) ===\n", *minScore, len(leads))
	for _, l := range leads {
		fmt.Printf("\n[%d] (Score: %d) %s\n", l.ID, l.Score, l.Title)
		fmt.Printf("Source: %s | URL: %s\n", l.Source, l.URL)
		fmt.Printf("Reason: %s\n", l.ScoreReason)
		if l.DraftReply != "" {
			fmt.Printf("Draft reply:\n%s\n", l.DraftReply)
		}
		fmt.Println("-------------------------------------------")
	}
}
