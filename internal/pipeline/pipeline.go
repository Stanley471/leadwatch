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
	bio score.DeveloperBio,
	notifier *notify.Notifier,
) *Pipeline {
	return &Pipeline{
		cfg:        cfg,
		store:      st,
		sources:    sources,
		classifier: classify.New(),
		scorer:     score.New(bio),
		drafter:    draft.New(bio),
		notifier:   notifier,
	}
}

// RunCycle executes all pipeline stages idempotently.
func (p *Pipeline) RunCycle(ctx context.Context) error {
	slog.Info("starting pipeline cycle")

	if err := p.StagePoll(ctx); err != nil {
		slog.Error("stage poll error", "err", err)
	}

	if err := p.StagePrefilter(ctx); err != nil {
		slog.Error("stage prefilter error", "err", err)
	}

	if err := p.StageClassify(ctx); err != nil {
		slog.Error("stage classify error", "err", err)
	}

	if err := p.StageScore(ctx); err != nil {
		slog.Error("stage score error", "err", err)
	}

	if err := p.StageDraft(ctx); err != nil {
		slog.Error("stage draft error", "err", err)
	}

	if err := p.StageNotify(ctx); err != nil {
		slog.Error("stage notify error", "err", err)
	}

	slog.Info("pipeline cycle completed")
	return nil
}

// StagePoll polls all registered sources and inserts raw posts into store.
func (p *Pipeline) StagePoll(ctx context.Context) error {
	since := time.Now().Add(-24 * time.Hour)
	for _, src := range p.sources {
		slog.Info("polling source", "source", src.Name())
		posts, err := src.Poll(ctx, since)
		if err != nil {
			slog.Error("source polling failed", "source", src.Name(), "err", err)
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
				slog.Error("error upserting post", "id", raw.ExternalID, "err", err)
				continue
			}
			if ok {
				inserted++
			}
		}
		slog.Info("poll finished", "source", src.Name(), "found", len(posts), "new", inserted)
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
			slog.Error("failed to save classification", "id", post.ID, "err", err)
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
		if res.Score < 50 {
			status = store.StatusIgnored
		}

		if err := p.store.SaveScore(ctx, post.ID, res.Score, res.Reason, status); err != nil {
			slog.Error("failed to save score", "id", post.ID, "err", err)
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
			slog.Error("failed to save draft", "id", post.ID, "err", err)
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
				slog.Error("failed to send notification", "id", post.ID, "err", err)
				continue
			}
		}
		if err := p.store.MarkNotified(ctx, post.ID, time.Now().UTC(), store.StatusNotified); err != nil {
			slog.Error("failed to mark notified", "id", post.ID, "err", err)
		}
		slog.Info("alert sent to owner for lead", "id", post.ID, "title", post.Title, "score", post.Score)
	}
	return nil
}
