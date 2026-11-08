package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

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
	case "lead":
		runLeadDetail(ctx, cfg, os.Args[2:])
	case "mark":
		runMarkStatus(ctx, cfg, os.Args[2:])
	case "profile":
		runProfile(ctx, cfg, os.Args[2:])
	case "portfolio":
		runPortfolio(ctx, cfg, os.Args[2:])
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
  migrate        Initialize or apply migrations to the SQLite database
  poll           Run a single ingestion and pipeline cycle then exit
  run            Start leadwatch background daemon loop
  leads          List leads in a table (--status, --min-score)
  lead <id>      View full details, classification, and drafted reply for a lead
  mark <id> <st> Mark lead status: contacted, replied, won, lost, ignored
  profile set    Update developer profile (services, rate floor, location, tone)
  portfolio add  Register an authentic completed project in portfolio

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
	statusFlag := fs.String("status", "", "filter by status (e.g. new, to_classify, scored, drafted, notified, contacted)")
	minScoreFlag := fs.Int("min-score", 60, "minimum score threshold")
	limitFlag := fs.Int("limit", 50, "maximum leads to display")
	_ = fs.Parse(args)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	leads, err := st.ListLeadsFiltered(ctx, *statusFlag, *minScoreFlag, *limitFlag)
	if err != nil {
		slog.Error("querying leads failed", "err", err)
		os.Exit(1)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tSCORE\tSOURCE\tSTATUS\tOUTREACH\tFOLLOW-UP\tTITLE\n")
	fmt.Fprintf(w, "--\t-----\t------\t------\t--------\t---------\t-----\n")

	for _, l := range leads {
		outreach, _ := st.GetOutreach(ctx, l.ID)
		outreachStatus := "-"
		followUpStr := "-"
		if outreach != nil {
			outreachStatus = outreach.Status
			if outreach.FollowUpDue != nil {
				followUpStr = outreach.FollowUpDue.Format("2006-01-02")
			}
		}

		title := l.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}

		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
			l.ID, l.Score, l.Source, l.Status, outreachStatus, followUpStr, title,
		)
	}
	w.Flush()
}

func runLeadDetail(ctx context.Context, cfg *config.Config, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: leadwatch lead <id>")
		os.Exit(1)
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Printf("invalid lead id: %s\n", args[0])
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	post, err := st.GetPostByID(ctx, id)
	if err != nil || post == nil {
		fmt.Printf("lead %d not found\n", id)
		os.Exit(1)
	}

	classRec, _ := st.GetDetailedClassification(ctx, id)
	outreach, _ := st.GetOutreach(ctx, id)

	fmt.Printf("\n=== LEAD DETAIL [#%d] ===\n", post.ID)
	fmt.Printf("Title:       %s\n", post.Title)
	fmt.Printf("Author:      %s\n", post.Author)
	fmt.Printf("Source:      %s\n", post.Source)
	fmt.Printf("URL:         %s\n", post.URL)
	fmt.Printf("Published:   %s\n", post.PublishedAt.Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("Status:      %s\n", post.Status)
	fmt.Printf("Score:       %d/100 (%s)\n", post.Score, post.ScoreReason)

	if classRec != nil {
		fmt.Println("\n--- Classification ---")
		fmt.Printf("Project Type: %s\n", classRec.ProjectType)
		if classRec.BudgetAmount != nil {
			curr := "USD"
			if classRec.BudgetCurrency != nil {
				curr = *classRec.BudgetCurrency
			}
			fmt.Printf("Budget:       %.0f %s\n", *classRec.BudgetAmount, curr)
		}
		fmt.Printf("Urgency:      %s\n", classRec.Urgency)
		fmt.Printf("Tech:         %s\n", classRec.Tech)
		fmt.Printf("Scam Score:   %d/100\n", classRec.ScamScore)
		fmt.Printf("Reason:       %s\n", classRec.Reason)
	}

	fmt.Println("\n--- Original Post Body ---")
	fmt.Println(post.Body)

	fmt.Println("\n--- Drafted Proposal Reply ---")
	draftReply := post.DraftReply
	channel := "email"
	status := "drafted"
	if outreach != nil {
		if outreach.DraftReply != "" {
			draftReply = outreach.DraftReply
		}
		channel = outreach.Channel
		status = outreach.Status
	}
	fmt.Printf("Channel: %s | Status: %s\n\n", channel, status)
	if draftReply != "" {
		fmt.Println(draftReply)
	} else {
		fmt.Println("[No draft generated yet]")
	}
	fmt.Println("========================================")
}

func runMarkStatus(ctx context.Context, cfg *config.Config, args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: leadwatch mark <id> <contacted|replied|won|lost|ignored>")
		os.Exit(1)
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Printf("invalid lead id: %s\n", args[0])
		os.Exit(1)
	}

	status := strings.ToLower(args[1])
	switch status {
	case "contacted", "replied", "won", "lost", "ignored":
	default:
		fmt.Printf("invalid status '%s'. Must be one of: contacted, replied, won, lost, ignored\n", status)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.MarkOutreachStatus(ctx, id, status); err != nil {
		fmt.Fprintf(os.Stderr, "failed to update status: %v\n", err)
		os.Exit(1)
	}

	if status == "contacted" {
		fmt.Printf("✓ Lead #%d marked as 'contacted'. Follow-up due scheduled for %s (+3 days).\n",
			id, time.Now().Add(3*24*time.Hour).Format("2006-01-02"),
		)
	} else {
		fmt.Printf("✓ Lead #%d marked as '%s'.\n", id, status)
	}
}

func runProfile(ctx context.Context, cfg *config.Config, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: leadwatch profile set [--services 'web_app, ecommerce'] [--rate-floor 1000] [--location 'remote'] [--tone 'professional']")
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if args[0] == "set" {
		fs := flag.NewFlagSet("profile set", flag.ExitOnError)
		services := fs.String("services", "web_app, ecommerce, landing_site, maintenance", "comma-separated services")
		rateFloor := fs.Float64("rate-floor", 500.0, "rate floor in USD")
		location := fs.String("location", "remote, worldwide", "location preferences")
		tone := fs.String("tone", "professional, friendly, and concise", "proposal writing tone")
		_ = fs.Parse(args[1:])

		p := store.ProfileConfig{
			Services:  *services,
			RateFloor: *rateFloor,
			Location:  *location,
			Tone:      *tone,
		}
		if err := st.SetProfileSettings(ctx, p); err != nil {
			fmt.Fprintf(os.Stderr, "failed to save profile: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Developer profile updated successfully.")
	} else {
		p, _ := st.GetProfileSettings(ctx)
		fmt.Printf("Services:   %s\n", p.Services)
		fmt.Printf("Rate Floor: $%.0f\n", p.RateFloor)
		fmt.Printf("Location:   %s\n", p.Location)
		fmt.Printf("Tone:       %s\n", p.Tone)
	}
}

func runPortfolio(ctx context.Context, cfg *config.Config, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: leadwatch portfolio add --title 'Project' --url 'https://...' --desc '...' --tags 'react,go'")
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if args[0] == "add" {
		fs := flag.NewFlagSet("portfolio add", flag.ExitOnError)
		title := fs.String("title", "", "project title")
		url := fs.String("url", "", "project public URL")
		desc := fs.String("desc", "", "project description and scope")
		tags := fs.String("tags", "", "comma-separated tech tags")
		_ = fs.Parse(args[1:])

		if *title == "" || *url == "" {
			fmt.Println("error: --title and --url are required")
			os.Exit(1)
		}

		item := store.PortfolioItem{
			Title:       *title,
			URL:         *url,
			Description: *desc,
			Tags:        *tags,
		}
		if err := st.AddPortfolioItem(ctx, item); err != nil {
			fmt.Fprintf(os.Stderr, "failed to add portfolio item: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Portfolio item '%s' added successfully.\n", *title)
	} else {
		items, _ := st.ListPortfolioItems(ctx)
		fmt.Printf("Registered Portfolio Items (%d):\n", len(items))
		for _, it := range items {
			fmt.Printf("- [%d] %s (%s) [tags: %s]\n  %s\n", it.ID, it.Title, it.URL, it.Tags, it.Description)
		}
	}
}
