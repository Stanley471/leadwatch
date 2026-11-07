package draft_test

import (
	"context"
	"strings"
	"testing"

	"leadwatch/internal/config"
	"leadwatch/internal/draft"
	"leadwatch/internal/store"
)

func TestDrafter(t *testing.T) {
	bio := config.DeveloperBio{
		Name:      "Jane Doe",
		Title:     "Senior Fullstack Engineer",
		Skills:    []string{"Go", "React", "PostgreSQL"},
		Portfolio: "https://janedoe.dev",
	}
	d := draft.New(bio)

	post := store.Post{
		Title:  "Need Go & React Developer",
		Author: "Alex",
		Body:   "Looking for a contractor to help build our MVP.",
	}

	reply := d.Draft(context.Background(), &post)

	if !strings.Contains(reply, "Hi Alex,") {
		t.Errorf("expected greeting to Alex, got: %s", reply)
	}
	if !strings.Contains(reply, "Jane Doe") {
		t.Errorf("expected signature with Jane Doe, got: %s", reply)
	}
	if !strings.Contains(reply, "https://janedoe.dev") {
		t.Errorf("expected portfolio link, got: %s", reply)
	}
}
