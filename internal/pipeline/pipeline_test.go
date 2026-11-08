package pipeline_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"leadwatch/internal/config"
	"leadwatch/internal/pipeline"
	"leadwatch/internal/score"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

func TestNormalizePost(t *testing.T) {
	longBody := strings.Repeat("A", 10000)
	post := &store.Post{
		Title:  "  Senior   Web   Developer   \n Needed  ",
		Author: "  \t client_123 \n ",
		Body:   "   Here is  \t\n  my text.   " + longBody,
		URL:    "HTTPS://Example.COM:443/jobs/123?utm_source=twitter&ref=newsletter&id=abc#heading",
	}

	pipeline.NormalizePost(post)

	if post.Title != "Senior Web Developer Needed" {
		t.Errorf("unexpected title: %q", post.Title)
	}
	if post.Author != "client_123" {
		t.Errorf("unexpected author: %q", post.Author)
	}
	if len(post.Body) > pipeline.MaxBodyLength {
		t.Errorf("expected body length <= %d, got %d", pipeline.MaxBodyLength, len(post.Body))
	}
	if strings.Contains(post.URL, "utm_source") || strings.Contains(post.URL, "ref") || strings.Contains(post.URL, "#heading") {
		t.Errorf("expected clean canonical URL, got %s", post.URL)
	}
	if !strings.HasPrefix(post.URL, "https://example.com/jobs/123") {
		t.Errorf("expected canonicalized scheme and host, got %s", post.URL)
	}
}

func TestPrefilterPost_TableDriven(t *testing.T) {
	cfg := config.DefaultPrefilterConfig()

	tests := []struct {
		name     string
		post     store.Post
		expected store.Status
	}{
		{
			name: "Clear hiring intent with [Hiring]",
			post: store.Post{
				Title: "[Hiring] Go backend engineer",
				Body:  "We need a backend developer with Go experience.",
			},
			expected: store.StatusToClassify,
		},
		{
			name: "Clear hiring intent with budget",
			post: store.Post{
				Title: "Seeking website developer",
				Body:  "Have a $5k budget to build me a custom shop.",
			},
			expected: store.StatusToClassify,
		},
		{
			name: "Want to develop app",
			post: store.Post{
				Title: "Looking for a partner to develop our MVP",
				Body:  "Need quote for React application.",
			},
			expected: store.StatusToClassify,
		},
		{
			name: "Spam [For Hire] filter",
			post: store.Post{
				Title: "[For Hire] Full stack engineer available",
				Body:  "Need work, looking for clients.",
			},
			expected: store.StatusFilteredOut,
		},
		{
			name: "Spam DM me for services",
			post: store.Post{
				Title: "Need a web project? DM me for services",
				Body:  "I will build your site in 24 hours.",
			},
			expected: store.StatusFilteredOut,
		},
		{
			name: "Self promo I will build",
			post: store.Post{
				Title: "I will build responsive websites",
				Body:  "Seeking projects to work on.",
			},
			expected: store.StatusFilteredOut,
		},
		{
			name: "Completely irrelevant text without intent",
			post: store.Post{
				Title: "Discussion about new laptop released yesterday",
				Body:  "The battery life is really good and screen is bright.",
			},
			expected: store.StatusFilteredOut,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pipeline.PrefilterPost(&tt.post, cfg)
			if got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

// MockSource provides a deterministic in-memory source for pipeline integration testing.
type mockSource struct {
	name  string
	posts []source.RawPost
}

func (m *mockSource) Name() string {
	return m.name
}

func (m *mockSource) Poll(ctx context.Context, since time.Time) ([]source.RawPost, error) {
	var filtered []source.RawPost
	for _, p := range m.posts {
		if since.IsZero() || p.PostedAt.After(since) {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

func TestPipeline_RunOnce_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "pipeline_test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	mockPosts := []source.RawPost{
		{
			ExternalID: "post_1",
			URL:        "https://example.com/p1?utm_source=twitter",
			Title:      "  [Hiring] Need a Web Developer  ",
			Author:     "lead_buyer",
			Body:       "We have a high budget to develop an MVP.",
			PostedAt:   now.Add(-2 * time.Hour),
		},
		{
			ExternalID: "post_2",
			URL:        "https://example.com/p2",
			Title:      "[For Hire] Web Developer looking for work",
			Author:     "freelancer_bob",
			Body:       "DM me for services. I will build anything.",
			PostedAt:   now.Add(-1 * time.Hour),
		},
		{
			ExternalID: "post_3",
			URL:        "https://example.com/p3",
			Title:      "Random off-topic chat",
			Author:     "chatter",
			Body:       "Just saying hello to the community.",
			PostedAt:   now.Add(-30 * time.Minute),
		},
	}

	src := &mockSource{
		name:  "mock_src",
		posts: mockPosts,
	}

	cfg := &config.Config{
		Prefilter: config.DefaultPrefilterConfig(),
	}

	pipe := pipeline.New(cfg, st, []source.Source{src}, score.DefaultBio(), nil)
	ctx := context.Background()

	// First Run: should poll, insert 3 posts as "new", and prefilter them
	if err := pipe.RunOnce(ctx, "mock_src"); err != nil {
		t.Fatalf("first RunOnce failed: %v", err)
	}

	toClassifyPosts, err := st.GetPostsByStatus(ctx, store.StatusToClassify, 10)
	if err != nil || len(toClassifyPosts) != 1 {
		t.Fatalf("expected 1 post in to_classify (post_1), got %d, err=%v", len(toClassifyPosts), err)
	}
	if toClassifyPosts[0].ExternalID != "post_1" {
		t.Errorf("expected post_1 in to_classify, got %s", toClassifyPosts[0].ExternalID)
	}
	if toClassifyPosts[0].URL != "https://example.com/p1" {
		t.Errorf("expected canonicalized URL without utm params, got %s", toClassifyPosts[0].URL)
	}

	filteredOutPosts, err := st.GetPostsByStatus(ctx, store.StatusFilteredOut, 10)
	if err != nil || len(filteredOutPosts) != 2 {
		t.Fatalf("expected 2 posts in filtered_out, got %d, err=%v", len(filteredOutPosts), err)
	}

	// Verify persistent cursor in `sources` table
	lastPolled, err := st.GetSourceLastPolledAt(ctx, "mock_src")
	if err != nil || lastPolled.IsZero() {
		t.Fatalf("expected last_polled_at cursor to be set, got %v, err=%v", lastPolled, err)
	}

	// Second Run: cursor prevents re-polling past posts, nothing new inserted
	if err := pipe.RunOnce(ctx, "mock_src"); err != nil {
		t.Fatalf("second RunOnce failed: %v", err)
	}

	// Verify post counts haven't changed
	totalCount, err := st.TotalCount(ctx)
	if err != nil || totalCount != 3 {
		t.Fatalf("expected total posts to remain 3 on second run, got %d, err=%v", totalCount, err)
	}
}
