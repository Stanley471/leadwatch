package classify_test

import (
	"context"
	"testing"

	"leadwatch/internal/classify"
	"leadwatch/internal/store"
)

func TestClassifier(t *testing.T) {
	c := classify.New()

	tests := []struct {
		name     string
		post     store.Post
		isHiring bool
		category string
	}{
		{
			name: "Authentic hiring post for web developer",
			post: store.Post{
				Source: "hackernews",
				Title:  "Startup hiring Fullstack Web Developer (Go / React)",
				Body:   "We are looking for an experienced web engineer to build our core APIs and UI.",
			},
			isHiring: true,
			category: "web_development",
		},
		{
			name: "Job seeker looking for work",
			post: store.Post{
				Source: "reddit",
				Title:  "[FOR HIRE] Web Developer looking for work",
				Body:   "Available for freelance React and Go projects. Check out my portfolio.",
			},
			isHiring: false,
			category: "job_seeker",
		},
		{
			name: "Irrelevant role",
			post: store.Post{
				Source: "hackernews",
				Title:  "We are hiring a CPA Accountant",
				Body:   "Audit and tax reporting experience required.",
			},
			isHiring: false,
			category: "other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := c.Classify(context.Background(), &tt.post)
			if res.IsHiring != tt.isHiring {
				t.Errorf("expected isHiring=%v, got %v", tt.isHiring, res.IsHiring)
			}
			if res.Category != tt.category {
				t.Errorf("expected category=%s, got %s", tt.category, res.Category)
			}
		})
	}
}
