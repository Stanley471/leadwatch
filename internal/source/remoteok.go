package source

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type RemoteOKSource struct {
	client  *Client
	baseURL string
}

func NewRemoteOKSource(client *Client) *RemoteOKSource {
	if client == nil {
		client = NewClient(nil)
	}
	return &RemoteOKSource{
		client:  client,
		baseURL: "https://remoteok.com/api",
	}
}

func (s *RemoteOKSource) SetBaseURL(u string) {
	s.baseURL = u
}

func (s *RemoteOKSource) Name() string {
	return "remoteok"
}

type remoteOKItem struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Epoch       int64    `json:"epoch"`
	Date        string   `json:"date"`
	Company     string   `json:"company"`
	Position    string   `json:"position"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
}

func (s *RemoteOKSource) Poll(ctx context.Context, since time.Time) ([]RawPost, error) {
	data, err := s.client.Get(ctx, s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("remoteok poll: %w", err)
	}

	var items []remoteOKItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("remoteok parse: %w", err)
	}

	var posts []RawPost
	for _, item := range items {
		if item.ID == "" || item.Position == "" {
			continue
		}

		var pubTime time.Time
		if item.Epoch > 0 {
			pubTime = time.Unix(item.Epoch, 0).UTC()
		} else if item.Date != "" {
			pubTime, _ = time.Parse(time.RFC3339, item.Date)
		} else {
			pubTime = time.Now().UTC()
		}

		if !since.IsZero() && pubTime.Before(since) {
			continue
		}

		itemURL := item.URL
		if itemURL == "" {
			itemURL = fmt.Sprintf("https://remoteok.com/remote-jobs/%s", item.Slug)
		}

		body := stripHTML(item.Description)
		title := fmt.Sprintf("%s at %s", item.Position, item.Company)

		rawItem, _ := json.Marshal(item)

		posts = append(posts, RawPost{
			ExternalID: item.ID,
			URL:        itemURL,
			Title:      title,
			Author:     item.Company,
			Body:       body,
			PostedAt:   pubTime,
			Raw:        rawItem,
		})
	}

	return posts, nil
}
