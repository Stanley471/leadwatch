package source_test

import (
	"context"
	_ "embed"
	"net/http"
	"net/http/httptest"
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

func TestHNSource_Poll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Generic User-Agent check
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
