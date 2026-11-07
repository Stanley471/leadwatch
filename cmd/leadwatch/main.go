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
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

func main() {
	configPath := flag.String("config", "leadwatch.json", "path to config file")
	once := flag.Bool("once", false, "run one pipeline cycle and exit")
	listLeads := flag.Bool("list", false, "list discovered high-scoring leads and exit")
	minScore := flag.Int("min-score", 50, "minimum score filter for listing leads")
	flag.Parse()

	// Configure structured logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("Failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if *listLeads {
		leads, err := st.ListLatestLeads(ctx, *minScore, 50)
		if err != nil {
			slog.Error("Failed to query leads", "err", err)
			os.Exit(1)
		}
		fmt.Printf("=== DISCOVERED LEADS (Min Score: %d) ===\n", *minScore)
		for _, l := range leads {
			fmt.Printf("\n[%d] (Score: %d) %s\n", l.ID, l.Score, l.Title)
			fmt.Printf("Source: %s | URL: %s\n", l.Source, l.URL)
			fmt.Printf("Reason: %s\n", l.ScoreReason)
			if l.DraftReply != "" {
				fmt.Printf("Draft reply:\n%s\n", l.DraftReply)
			}
			fmt.Println("-------------------------------------------")
		}
		return
	}

	// Register sources
	var sources []source.Source
	client := source.NewClient(nil)
	if cfg.Sources.HackerNewsJobs {
		sources = append(sources, source.NewHNSource(client))
	}
	if cfg.Sources.RemoteOK {
		sources = append(sources, source.NewRemoteOKSource(client))
	}
	if cfg.Sources.WWR {
		sources = append(sources, source.NewWWRSource(client))
	}

	notifier := notify.New(cfg.Telegram, nil)
	pipe := pipeline.New(cfg, st, sources, notifier)

	if *once {
		slog.Info("Running single cycle mode")
		if err := pipe.RunCycle(ctx); err != nil {
			slog.Error("Pipeline cycle failed", "err", err)
			os.Exit(1)
		}
		slog.Info("Cycle completed successfully")
		return
	}

	sched := scheduler.New(pipe, cfg.PollInterval)
	if err := sched.Start(ctx); err != nil && err != context.Canceled {
		slog.Error("Scheduler stopped with error", "err", err)
		os.Exit(1)
	}
}
