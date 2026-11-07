package score_test

import (
	"context"
	"testing"

	"leadwatch/internal/score"
	"leadwatch/internal/store"
)

func TestScorer(t *testing.T) {
	bio := score.DeveloperBio{
		Skills:   []string{"Go", "React", "TypeScript"},
		MinScore: 50,
	}
	s := score.New(bio)

	tests := []struct {
		name     string
		post     store.Post
		minScore int
	}{
		{
			name: "Perfect matching contract lead",
			post: store.Post{
				IsHiring:   true,
				Confidence: 1.0,
				Title:      "Urgent Remote Contract: Senior Go & React Developer",
				Body:       "High budget, looking for TypeScript and Go contractor immediately.",
			},
			minScore: 50,
		},
		{
			name: "Unpaid intern post",
			post: store.Post{
				IsHiring:   true,
				Confidence: 1.0,
				Title:      "Unpaid intern web developer",
				Body:       "Great experience for college students.",
			},
			minScore: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := s.Score(context.Background(), &tt.post)
			if tt.name == "Perfect matching contract lead" && res.Score < tt.minScore {
				t.Errorf("expected score >= %d, got %d", tt.minScore, res.Score)
			}
			if tt.name == "Unpaid intern post" && res.Score > 20 {
				t.Errorf("expected low score for unpaid lead, got %d", res.Score)
			}
		})
	}
}
