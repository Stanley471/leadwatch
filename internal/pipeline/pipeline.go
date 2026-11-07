package pipeline

import (
	"context"
	"log/slog"
	"time"

	"leadwatch/internal/classify"
	"leadwatch/internal/config"
	"leadwatch/internal/draft"
	"leadwatch/internal/notify"
	"leadwatch/internal/score"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

// Pipeline coordinates stages:
// poll → normalize/dedupe → prefilter → classify → score → draft → notify
type Pipeline struct {
	cfg        *config.Config
	store      *store.Store
	sources    []source.Source
	classifier *classify.Classifier
	scorer     *score.Scorer
	drafter    *draft.Drafter
	notifier   *notify.Notifier
}

func New(
	cfg *config.Config,
	st *store.Store,
	sources []source.Source,
	notifier *notify.Notifier,
) *Pipeline {
	return &Pipeline{
		cfg:        cfg,
		store:      st,
		sources:    sources,
		classifier: classify.New(),
		scorer:     score.New(cfg.Developer),
		drafter:    draft.New(cfg.Developer),
		notifier:   notifier,
	}
}

// RunCycle executes all pipeline stages idempotently.
func (p *Pipeline) RunCycle(ctx context.Context) error {
	slog.Info("Starting pipeline cycle...")

	// 1. Stage: Poll & Dedupe
	if err := p.StagePoll(ctx); err != nil {
		slog.Error("Stage poll error", "err", err)
	}

	// 2. Stage: Prefilter
	if err := p.StagePrefilter(ctx); err != nil {
		slog.Error("Stage prefilter error", "err", err)
	}

	// 3. Stage: Classify
	if err := p.StageClassify(ctx); err != nil {
		slog.Error("Stage classify error", "err", err)
	}

	// 4. Stage: Score
	if err := p.StageScore(ctx); err != nil {
		slog.Error("Stage score error", "err", err)
	}

	// 5. Stage: Draft
	if err := p.StageDraft(ctx); err != nil {
		slog.Error("Stage draft error", "err", err)
	}

	// 6. Stage: Notify (Alerts owner on Telegram)
	if err := p.StageNotify(ctx); err != nil {
		slog.Error("Stage notify error", "err", err)
	}

	slog.Info("Pipeline cycle completed.")
	return nil
}

// StagePoll polls all registered sources and inserts raw posts into store.
func (p *Pipeline) StagePoll(ctx context.Context) error {
	since := time.Now().Add(-24 * time.Hour) // default backfill 24h
	for _, src := range p.sources {
		slog.Info("Polling source", "source", src.Name())
		posts, err := src.Poll(ctx, since)
		if err != nil {
			slog.Error("Source polling failed", "source", src.Name(), "err", err)
			continue
		}

		inserted := 0
		for _, raw := range posts {
			post := &store.Post{
				Source:      raw.Source,
				ExternalID:  raw.ExternalID,
				URL:         raw.URL,
				Title:       raw.Title,
				Author:      raw.Author,
				Body:        raw.Body,
				PublishedAt: raw.PublishedAt,
				Status:      store.StatusRaw,
			}
			ok, err := p.store.UpsertRaw(ctx, post)
			if err != nil {
				slog.Error("Error upserting post", "id", raw.ExternalID, "err", err)
				continue
			}
			if ok {
				inserted++
			}
		}
		slog.Info("Poll finished", "source", src.Name(), "found", len(posts), "new", inserted)
	}
	return nil
}

// StagePrefilter checks raw posts for bare minimum requirements.
func (p *Pipeline) StagePrefilter(ctx context.Context) error {
	posts, err := p.store.GetPostsByStatus(ctx, store.StatusRaw, 200)
	if err != nil {
		return err
	}

	for _, post := range posts {
		if post.Title == "" && post.Body == "" {
			_ = p.store.UpdateStatus(ctx, post.ID, store.StatusIgnored)
			continue
		}
		_ = p.store.UpdateStatus(ctx, post.ID, store.StatusPrefiltered)
	}
	return nil
}

// StageClassify classifies prefiltered posts.
func (p *Pipeline) StageClassify(ctx context.Context) error {
	posts, err := p.store.GetPostsByStatus(ctx, store.StatusPrefiltered, 200)
	if err != nil {
		return err
	}

	for _, post := range posts {
		res := p.classifier.Classify(ctx, post)
		post.IsHiring = res.IsHiring
		post.Category = res.Category
		post.Confidence = res.Confidence

		status := store.StatusClassified
		if !res.IsHiring {
			status = store.StatusIgnored
		}

		if err := p.store.SaveClassification(ctx, post.ID, res.IsHiring, res.Category, res.Confidence, status); err != nil {
			slog.Error("Failed to save classification", "id", post.ID, "err", err)
		}
	}
	return nil
}

// StageScore evaluates matches against developer bio.
func (p *Pipeline) StageScore(ctx context.Context) error {
	posts, err := p.store.GetPostsByStatus(ctx, store.StatusClassified, 200)
	if err != nil {
		return err
	}

	for _, post := range posts {
		res := p.scorer.Score(ctx, post)
		status := store.StatusScored
		if res.Score < p.cfg.Developer.MinScore {
			// Below interest threshold
			status = store.StatusIgnored
		}

		if err := p.store.SaveScore(ctx, post.ID, res.Score, res.Reason, status); err != nil {
			slog.Error("Failed to save score", "id", post.ID, "err", err)
		}
	}
	return nil
}

// StageDraft creates drafted proposal replies for scored leads.
func (p *Pipeline) StageDraft(ctx context.Context) error {
	posts, err := p.store.GetPostsByStatus(ctx, store.StatusScored, 100)
	if err != nil {
		return err
	}

	for _, post := range posts {
		draftText := p.drafter.Draft(ctx, post)
		if err := p.store.SaveDraft(ctx, post.ID, draftText, time.Now().UTC(), store.StatusDrafted); err != nil {
			slog.Error("Failed to save draft", "id", post.ID, "err", err)
		}
	}
	return nil
}

// StageNotify sends notification alert to owner on Telegram.
func (p *Pipeline) StageNotify(ctx context.Context) error {
	posts, err := p.store.GetPostsByStatus(ctx, store.StatusDrafted, 50)
	if err != nil {
		return err
	}

	for _, post := range posts {
		if p.notifier != nil {
			if err := p.notifier.NotifyAlert(ctx, post); err != nil {
				slog.Error("Failed to send notification", "id", post.ID, "err", err)
				continue
			}
		}
		if err := p.store.MarkNotified(ctx, post.ID, time.Now().UTC(), store.StatusNotified); err != nil {
			slog.Error("Failed to mark notified", "id", post.ID, "err", err)
		}
		slog.Info("Alert sent to owner for lead", "id", post.ID, "title", post.Title, "score", post.Score)
	}
	return nil
}
