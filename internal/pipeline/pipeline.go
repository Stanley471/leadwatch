package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"leadwatch/internal/classify"
	"leadwatch/internal/config"
	"leadwatch/internal/draft"
	"leadwatch/internal/notify"
	"leadwatch/internal/score"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

var whitespaceRegex = regexp.MustCompile(`\s+`)

const MaxBodyLength = 8000

// CanonicalizeURL parses and normalizes a URL by stripping tracking parameters, lowercase scheme and host, and removing default ports.
func CanonicalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	// Strip standard trailing default ports
	if strings.HasSuffix(u.Host, ":80") && u.Scheme == "http" {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	} else if strings.HasSuffix(u.Host, ":443") && u.Scheme == "https" {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}

	// Filter query parameters: strip common tracking params (utm_*, ref, etc.)
	if u.RawQuery != "" {
		q := u.Query()
		var keysToDelete []string
		for k := range q {
			lowerK := strings.ToLower(k)
			if strings.HasPrefix(lowerK, "utm_") || lowerK == "ref" || lowerK == "source" || lowerK == "fbclid" || lowerK == "gclid" {
				keysToDelete = append(keysToDelete, k)
			}
		}
		for _, k := range keysToDelete {
			q.Del(k)
		}
		u.RawQuery = q.Encode()
	}

	// Strip fragment
	u.Fragment = ""

	return u.String()
}

// NormalizePost cleans whitespace, caps body length at 8k chars, and canonicalizes the URL.
func NormalizePost(p *store.Post) {
	// Trim and collapse whitespace for Title
	p.Title = strings.TrimSpace(whitespaceRegex.ReplaceAllString(p.Title, " "))
	p.Author = strings.TrimSpace(whitespaceRegex.ReplaceAllString(p.Author, " "))

	// Collapse whitespace and trim Body
	body := strings.TrimSpace(whitespaceRegex.ReplaceAllString(p.Body, " "))
	if len(body) > MaxBodyLength {
		// Cut off cleanly at 8k limit
		body = body[:MaxBodyLength]
	}
	p.Body = body

	// Canonicalize URL
	p.URL = CanonicalizeURL(p.URL)
}

// PrefilterPost is a pure function that evaluates a post against prefilter keywords.
// It returns store.StatusFilteredOut when the text lacks hiring intent or contains spam/self-promo signals.
// Otherwise it returns store.StatusToClassify.
func PrefilterPost(p *store.Post, cfg config.PrefilterConfig) store.Status {
	fullText := strings.ToLower(p.Title + " " + p.Body)

	// 1. Check for spam / self-promo signals first
	for _, spam := range cfg.SpamSignals {
		if strings.Contains(fullText, strings.ToLower(spam)) {
			return store.StatusFilteredOut
		}
	}

	// 2. Check for hiring intent signals
	hasIntent := false
	for _, intent := range cfg.IntentSignals {
		if strings.Contains(fullText, strings.ToLower(intent)) {
			hasIntent = true
			break
		}
	}

	if !hasIntent {
		return store.StatusFilteredOut
	}

	return store.StatusToClassify
}

// Pipeline coordinates stages:
// poll → normalize/dedupe → prefilter → classify → score → draft → notify
type Pipeline struct {
	cfg        *config.Config
	store      *store.Store
	sources    []source.Source
	classifier classify.Classifier
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
	var c classify.Classifier
	if cfg.AnthropicAPIKey != "" {
		c = classify.NewAnthropicClassifier(cfg.AnthropicAPIKey, cfg.ClassifierModel, nil)
	} else {
		c = classify.NewFallbackRuleBasedClassifier()
	}

	return &Pipeline{
		cfg:        cfg,
		store:      st,
		sources:    sources,
		classifier: c,
		scorer:     score.New(score.DefaultProfile()),
		drafter:    draft.New(bio),
		notifier:   notifier,
	}
}

// SetClassifier allows overriding the classifier instance (e.g. for testing with FakeClassifier).
func (p *Pipeline) SetClassifier(c classify.Classifier) {
	p.classifier = c
}

// RunOnce executes a single polling, normalize/dedupe, and prefilter run across specified or all sources.
// It polls each source since its persistent last_polled_at cursor, inserts new posts as "new",
// runs the prefilter, and logs the counts per stage.
func (p *Pipeline) RunOnce(ctx context.Context, sourceFilter string) error {
	slog.Info("starting pipeline RunOnce", "sourceFilter", sourceFilter)

	var targetSources []source.Source
	sourceFilter = strings.ToLower(strings.TrimSpace(sourceFilter))

	for _, s := range p.sources {
		name := strings.ToLower(s.Name())
		if sourceFilter == "" || sourceFilter == "all" {
			targetSources = append(targetSources, s)
		} else if strings.Contains(name, sourceFilter) {
			targetSources = append(targetSources, s)
		}
	}

	if len(targetSources) == 0 {
		return fmt.Errorf("no matching sources found for filter '%s'", sourceFilter)
	}

	totalPolled := 0
	totalInserted := 0
	totalFilteredOut := 0
	totalToClassify := 0

	for _, src := range targetSources {
		name := src.Name()
		lastPolled, err := p.store.GetSourceLastPolledAt(ctx, name)
		if err != nil {
			slog.Error("failed to get last_polled_at for source", "source", name, "err", err)
			continue
		}

		pollStartTime := time.Now().UTC()
		slog.Info("polling source", "source", name, "since", lastPolled)
		rawPosts, err := src.Poll(ctx, lastPolled)
		if err != nil {
			slog.Error("error polling source", "source", name, "err", err)
			continue
		}

		totalPolled += len(rawPosts)
		newInsertedForSource := 0

		for _, raw := range rawPosts {
			post := &store.Post{
				Source:      name,
				ExternalID:  raw.ExternalID,
				URL:         raw.URL,
				Title:       raw.Title,
				Author:      raw.Author,
				Body:        raw.Body,
				PublishedAt: raw.PostedAt,
			}

			// 1. Normalize
			NormalizePost(post)

			// 2. Insert with stage "new"
			inserted, err := p.store.InsertPostIfNew(ctx, post)
			if err != nil {
				slog.Error("error inserting new post", "source", name, "id", raw.ExternalID, "err", err)
				continue
			}

			if inserted {
				newInsertedForSource++
				totalInserted++
			}
		}

		// Update persistent cursor
		if err := p.store.SetSourceLastPolledAt(ctx, name, pollStartTime); err != nil {
			slog.Error("failed to update last_polled_at for source", "source", name, "err", err)
		}

		slog.Info("source polling finished", "source", name, "fetched", len(rawPosts), "new", newInsertedForSource)
	}

	// 3. Prefilter all posts with status "new"
	newPosts, err := p.store.GetPostsByStatus(ctx, store.StatusNew, 500)
	if err != nil {
		return fmt.Errorf("getting new posts: %w", err)
	}

	for _, post := range newPosts {
		nextStatus := PrefilterPost(post, p.cfg.Prefilter)
		if nextStatus == store.StatusFilteredOut {
			totalFilteredOut++
		} else if nextStatus == store.StatusToClassify {
			totalToClassify++
		}
		if err := p.store.UpdateStatus(ctx, post.ID, nextStatus); err != nil {
			slog.Error("failed to update post stage after prefilter", "id", post.ID, "status", nextStatus, "err", err)
		}
	}

	slog.Info("pipeline RunOnce completed",
		"polled", totalPolled,
		"stage_new_inserted", totalInserted,
		"stage_filtered_out", totalFilteredOut,
		"stage_to_classify", totalToClassify,
	)

	return nil
}

// PollSource polls a single source since its persistent cursor, normalizes and inserts posts as "new".
func (p *Pipeline) PollSource(ctx context.Context, src source.Source) (int, error) {
	name := src.Name()
	lastPolled, err := p.store.GetSourceLastPolledAt(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("getting last_polled_at for %s: %w", name, err)
	}

	pollStartTime := time.Now().UTC()
	slog.Info("polling source", "source", name, "since", lastPolled)
	rawPosts, err := src.Poll(ctx, lastPolled)
	if err != nil {
		return 0, fmt.Errorf("polling %s: %w", name, err)
	}

	insertedCount := 0
	for _, raw := range rawPosts {
		post := &store.Post{
			Source:      name,
			ExternalID:  raw.ExternalID,
			URL:         raw.URL,
			Title:       raw.Title,
			Author:      raw.Author,
			Body:        raw.Body,
			PublishedAt: raw.PostedAt,
		}

		NormalizePost(post)
		inserted, err := p.store.InsertPostIfNew(ctx, post)
		if err != nil {
			slog.Error("error inserting new post", "source", name, "id", raw.ExternalID, "err", err)
			continue
		}
		if inserted {
			insertedCount++
		}
	}

	if err := p.store.SetSourceLastPolledAt(ctx, name, pollStartTime); err != nil {
		slog.Error("failed to update last_polled_at for source", "source", name, "err", err)
	}

	slog.Info("source polling finished", "source", name, "fetched", len(rawPosts), "new", insertedCount)
	return insertedCount, nil
}

// ProcessDownstream executes stage-by-stage transitions:
// new -> prefiltered (filtered_out or to_classify) -> classified -> scored -> drafted -> notified
func (p *Pipeline) ProcessDownstream(ctx context.Context) error {
	// 1. Stage Prefilter (processes "new" posts)
	newPosts, err := p.store.GetPostsByStatus(ctx, store.StatusNew, 200)
	if err != nil {
		return fmt.Errorf("fetching new posts: %w", err)
	}

	for _, post := range newPosts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		nextStatus := PrefilterPost(post, p.cfg.Prefilter)
		if err := p.store.UpdateStatus(ctx, post.ID, nextStatus); err != nil {
			slog.Error("failed to update post stage after prefilter", "id", post.ID, "status", nextStatus, "err", err)
		}
	}

	// 2. Stage Classify (processes "to_classify" posts using classifier)
	if _, err := classify.ProcessToClassifyBatch(ctx, p.store, p.classifier, 100); err != nil {
		slog.Error("error during ProcessToClassifyBatch", "err", err)
	}

	// 3. Stage Score (processes "classified" posts)
	classifiedPosts, err := p.store.GetPostsByStatus(ctx, store.StatusClassified, 100)
	if err != nil {
		return fmt.Errorf("fetching classified posts: %w", err)
	}

	now := time.Now().UTC()
	for _, post := range classifiedPosts {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		classRec, _ := p.store.GetDetailedClassification(ctx, post.ID)
		res := p.scorer.ScorePost(post, classRec, now)

		// Posts below the floor are stored in the DB, but ignored for drafting/alerting
		status := store.StatusScored
		if res.Score < 50 {
			status = store.StatusIgnored
		}
		_ = p.store.SaveScore(ctx, post.ID, res.Score, res.Reason, status)
	}

	// 4. Stage Draft (processes "scored" posts)
	scoredPosts, err := p.store.GetPostsByStatus(ctx, store.StatusScored, 50)
	if err != nil {
		return fmt.Errorf("fetching scored posts: %w", err)
	}

	for _, post := range scoredPosts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		draftText := p.drafter.Draft(ctx, post)
		_ = p.store.SaveDraft(ctx, post.ID, draftText, time.Now().UTC(), store.StatusDrafted)
	}

	// 5. Stage Notify (processes "drafted" posts, alerts owner on Telegram)
	if p.notifier != nil {
		if _, err := notify.ProcessAlerts(ctx, p.store, p.notifier, 50, 50); err != nil {
			slog.Error("error during ProcessAlerts", "err", err)
		}
	}

	return nil
}
