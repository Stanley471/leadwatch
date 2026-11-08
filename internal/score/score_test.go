package score_test

import (
	"testing"
	"time"

	"leadwatch/internal/score"
	"leadwatch/internal/store"
)

func TestCalculateScore_TableDriven(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	profile := score.DefaultProfile()
	profile.RateFloor = 1000.0
	weights := score.DefaultWeights()

	budget4500 := 4500.0
	budget100 := 100.0
	currency := "USD"
	remoteTrue := true

	tests := []struct {
		name           string
		post           store.Post
		classification *store.ClassificationRecord
		minScore       int
		maxScore       int
	}{
		{
			name: "Recent, well-budgeted, good fit project",
			post: store.Post{
				PublishedAt: now.Add(-30 * time.Minute), // Fresh (< 2h)
				Title:       "Need fullstack Go and React developer for SaaS web_app",
				Body:        "We have detailed requirements and scope. Looking for an experienced developer to build the database, API, and frontend components. High budget.",
			},
			classification: &store.ClassificationRecord{
				IsRequest:      true,
				ProjectType:    "web_app",
				BudgetAmount:   &budget4500,
				BudgetCurrency: &currency,
				RemoteOK:       &remoteTrue,
				Tech:           "Go,React,PostgreSQL",
				ScamScore:      0,
				Confidence:     95,
			},
			minScore: 80,
			maxScore: 100,
		},
		{
			name: "Older post (> 2h decay)",
			post: store.Post{
				PublishedAt: now.Add(-14 * time.Hour), // Decay after 2h
				Title:       "Need fullstack Go developer",
				Body:        "Requirements and stack experience needed for database and API.",
			},
			classification: &store.ClassificationRecord{
				IsRequest:      true,
				ProjectType:    "web_app",
				BudgetAmount:   &budget4500,
				BudgetCurrency: &currency,
				RemoteOK:       &remoteTrue,
				Tech:           "Go",
				ScamScore:      0,
				Confidence:     90,
			},
			minScore: 50,
			maxScore: 78,
		},
		{
			name: "Low budget below rate floor",
			post: store.Post{
				PublishedAt: now.Add(-1 * time.Hour),
				Title:       "Need small website built",
				Body:        "Simple landing page for my new club.",
			},
			classification: &store.ClassificationRecord{
				IsRequest:      true,
				ProjectType:    "landing_site",
				BudgetAmount:   &budget100, // significantly below $1000 floor
				BudgetCurrency: &currency,
				RemoteOK:       &remoteTrue,
				Tech:           "HTML",
				ScamScore:      0,
				Confidence:     85,
			},
			minScore: 30,
			maxScore: 65,
		},
		{
			name: "High scam score deduction",
			post: store.Post{
				PublishedAt: now.Add(-15 * time.Minute),
				Title:       "Urgent: make site for $50",
				Body:        "Contact me on telegram now. Pay before deliver.",
			},
			classification: &store.ClassificationRecord{
				IsRequest:      true,
				ProjectType:    "landing_site",
				BudgetAmount:   &budget100,
				BudgetCurrency: &currency,
				RemoteOK:       &remoteTrue,
				Tech:           "HTML",
				ScamScore:      80, // Heavy 80 point penalty
				Confidence:     90,
			},
			minScore: 0,
			maxScore: 25,
		},
		{
			name: "Not a hiring request (is_request=false)",
			post: store.Post{
				PublishedAt: now.Add(-10 * time.Minute),
				Title:       "[For Hire] Senior Go developer available",
				Body:        "10 years experience building websites and apps.",
			},
			classification: &store.ClassificationRecord{
				IsRequest:  false, // developer self promo
				ScamScore:  0,
				Confidence: 95,
			},
			minScore: 0,
			maxScore: 0, // Must be 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := score.CalculateScore(&tt.post, tt.classification, profile, weights, now)
			if res.Score < tt.minScore || res.Score > tt.maxScore {
				t.Errorf("expected score between %d and %d, got %d (reason: %s)",
					tt.minScore, tt.maxScore, res.Score, res.Reason,
				)
			}
		})
	}
}
