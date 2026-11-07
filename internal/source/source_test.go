package source_test

import (
	"context"
	_ "embed"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leadwatch/internal/source"
)

//go:embed testdata/hn_jobstories.json
var hnJobStoriesFixture []byte

//go:embed testdata/hn_item_101.json
var hnItem101Fixture []byte

//go:embed testdata/hn_item_102.json
var hnItem102Fixture []byte

//go:embed testdata/remoteok.json
var remoteOKFixture []byte

//go:embed testdata/wwr.xml
var wwrFixture []byte

//go:embed testdata/reddit_token.json
var redditTokenFixture []byte

//go:embed testdata/reddit_listing.json
var redditListingFixture []byte

//go:embed testdata/reddit_empty.json
var redditEmptyFixture []byte

//go:embed testdata/hn_algolia_threads.json
var hnAlgoliaThreadsFixture []byte

//go:embed testdata/hn_algolia_threads_empty.json
var hnAlgoliaThreadsEmptyFixture []byte

//go:embed testdata/hn_algolia_comments.json
var hnAlgoliaCommentsFixture []byte

func TestHNSource_Poll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != source.DefaultUserAgent {
			t.Errorf("expected user-agent %s, got %s", source.DefaultUserAgent, r.Header.Get("User-Agent"))
		}

		switch r.URL.Path {
		case "/jobstories.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write(hnJobStoriesFixture)
		case "/item/101.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write(hnItem101Fixture)
		case "/item/102.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write(hnItem102Fixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	src := source.NewHNSource(source.NewClient(server.Client()))
	src.SetBaseURL(server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posts) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(posts))
	}

	if posts[0].ExternalID != "101" {
		t.Errorf("expected ID 101, got %s", posts[0].ExternalID)
	}
	if posts[0].Author != "startup_inc" {
		t.Errorf("expected author startup_inc, got %s", posts[0].Author)
	}
}

func TestRemoteOKSource_Poll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != source.DefaultUserAgent {
			t.Errorf("expected user-agent %s, got %s", source.DefaultUserAgent, r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(remoteOKFixture)
	}))
	defer server.Close()

	src := source.NewRemoteOKSource(source.NewClient(server.Client()))
	src.SetBaseURL(server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}

	if posts[0].ExternalID != "rok-999" {
		t.Errorf("expected ID rok-999, got %s", posts[0].ExternalID)
	}
	if posts[0].Author != "Acme Inc" {
		t.Errorf("expected author Acme Inc, got %s", posts[0].Author)
	}
}

func TestWWRSource_Poll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != source.DefaultUserAgent {
			t.Errorf("expected user-agent %s, got %s", source.DefaultUserAgent, r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write(wwrFixture)
	}))
	defer server.Close()

	src := source.NewWWRSource(source.NewClient(server.Client()))
	src.SetFeedURL(server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}

	if posts[0].ExternalID != "wwr-12345" {
		t.Errorf("expected ID wwr-12345, got %s", posts[0].ExternalID)
	}
}

func TestRedditSource_Poll_NormalListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(redditTokenFixture)
			return
		}

		if r.URL.Path == "/r/forhire/new" {
			if r.Header.Get("Authorization") != "Bearer test_mock_token_12345" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(redditListingFixture)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	src := source.NewRedditSource(source.NewClient(server.Client()), source.RedditConfig{
		ClientID:     "mock_id",
		ClientSecret: "mock_secret",
		Subreddits:   []string{"forhire"},
	})
	src.SetBaseURLs(server.URL, server.URL+"/api/v1/access_token", server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posts) != 1 {
		t.Fatalf("expected 1 post after [Hiring] filter, got %d", len(posts))
	}

	if posts[0].ExternalID != "reddit_abc1" {
		t.Errorf("expected post ID reddit_abc1, got %s", posts[0].ExternalID)
	}
	if posts[0].Author != "client_dev" {
		t.Errorf("expected author client_dev, got %s", posts[0].Author)
	}
}

func TestRedditSource_Poll_EmptyListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(redditTokenFixture)
			return
		}

		if r.URL.Path == "/r/startups/new" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(redditEmptyFixture)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	src := source.NewRedditSource(source.NewClient(server.Client()), source.RedditConfig{
		ClientID:     "mock_id",
		ClientSecret: "mock_secret",
		Subreddits:   []string{"startups"},
	})
	src.SetBaseURLs(server.URL, server.URL+"/api/v1/access_token", server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posts) != 0 {
		t.Fatalf("expected 0 posts, got %d", len(posts))
	}
}

func TestRedditSource_Poll_RateLimitRetry(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(redditTokenFixture)
			return
		}

		if r.URL.Path == "/r/webdev/new" {
			cur := atomic.AddInt32(&attempts, 1)
			if cur == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"message": "Too Many Requests"}`))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.Write(redditEmptyFixture)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	src := source.NewRedditSource(source.NewClient(server.Client()), source.RedditConfig{
		ClientID:     "mock_id",
		ClientSecret: "mock_secret",
		Subreddits:   []string{"webdev"},
	})
	src.SetBaseURLs(server.URL, server.URL+"/api/v1/access_token", server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("expected success after retry, got err: %v", err)
	}

	if len(posts) != 0 {
		t.Errorf("expected 0 posts, got %d", len(posts))
	}

	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts (1 failure + 1 retry), got %d", atomic.LoadInt32(&attempts))
	}
}

func TestRedditSource_Poll_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(redditTokenFixture)
			return
		}

		if r.URL.Path == "/r/webdev/new" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{ "invalid": json {`))
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	src := source.NewRedditSource(source.NewClient(server.Client()), source.RedditConfig{
		ClientID:     "mock_id",
		ClientSecret: "mock_secret",
		Subreddits:   []string{"webdev"},
	})
	src.SetBaseURLs(server.URL, server.URL+"/api/v1/access_token", server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("expected graceful error handling without crashing, got: %v", err)
	}

	if len(posts) != 0 {
		t.Errorf("expected 0 posts on malformed json, got %d", len(posts))
	}
}

// 5. HN Freelancer source: Thread lookup & Comments page with HTML stripping & link preservation
func TestHNFreelancerSource_Poll_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tags := r.URL.Query().Get("tags")
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(tags, "ask_hn") {
			// Thread search lookup
			w.Write(hnAlgoliaThreadsFixture)
			return
		}

		if strings.Contains(tags, "comment,story_42000001") {
			// Comments page
			w.Write(hnAlgoliaCommentsFixture)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	src := source.NewHNFreelancerSource(source.NewClient(server.Client()))
	src.SetBaseURL(server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// In comments fixture:
	// - 42000010 (top-level SEEKING FREELANCER) -> included
	// - 42000011 (nested reply with parent_id 42000010) -> filtered out (not top-level)
	// - 42000012 (top-level SEEKING WORK) -> included (classifier handles filtering)
	if len(posts) != 2 {
		t.Fatalf("expected 2 top-level posts, got %d", len(posts))
	}

	// Verify post 1: 42000010
	p1 := posts[0]
	if p1.ExternalID != "hn_42000010" {
		t.Errorf("expected ID hn_42000010, got %s", p1.ExternalID)
	}
	if p1.Author != "tech_recruiter" {
		t.Errorf("expected author tech_recruiter, got %s", p1.Author)
	}
	if !strings.Contains(p1.Body, "our job post (https://example.com/apply)") {
		t.Errorf("expected link preserved in plain text body, got: %s", p1.Body)
	}

	// Verify post 2: 42000012
	p2 := posts[1]
	if p2.ExternalID != "hn_42000012" {
		t.Errorf("expected ID hn_42000012, got %s", p2.ExternalID)
	}
	if !strings.Contains(p2.Body, "https://portfolio.dev") {
		t.Errorf("expected link in p2, got: %s", p2.Body)
	}
}

// 6. HN Freelancer source: No thread found yet this month
func TestHNFreelancerSource_Poll_NoThreadFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(hnAlgoliaThreadsEmptyFixture)
	}))
	defer server.Close()

	src := source.NewHNFreelancerSource(source.NewClient(server.Client()))
	src.SetBaseURL(server.URL)

	posts, err := src.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posts) != 0 {
		t.Errorf("expected 0 posts when no thread exists, got %d", len(posts))
	}
}
