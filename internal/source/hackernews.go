package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type HNSource struct {
	client  *Client
	baseURL string
}

func NewHNSource(client *Client) *HNSource {
	if client == nil {
		client = NewClient(nil)
	}
	return &HNSource{
		client:  client,
		baseURL: "https://hacker-news.firebaseio.com/v0",
	}
}

func (s *HNSource) SetBaseURL(u string) {
	s.baseURL = u
}

func (s *HNSource) Name() string {
	return "hackernews"
}

type hnItem struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	By    string `json:"by"`
	Time  int64  `json:"time"`
	Text  string `json:"text"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (s *HNSource) Poll(ctx context.Context, since time.Time) ([]RawPost, error) {
	url := fmt.Sprintf("%s/jobstories.json", s.baseURL)
	data, err := s.client.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetching hn job stories: %w", err)
	}

	var ids []int64
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("parsing job story ids: %w", err)
	}

	var posts []RawPost
	maxJobs := 30
	if len(ids) < maxJobs {
		maxJobs = len(ids)
	}

	for i := 0; i < maxJobs; i++ {
		id := ids[i]
		itemURL := fmt.Sprintf("%s/item/%d.json", s.baseURL, id)
		itemData, err := s.client.Get(ctx, itemURL)
		if err != nil {
			continue
		}

		var item hnItem
		if err := json.Unmarshal(itemData, &item); err != nil {
			continue
		}

		pubTime := time.Unix(item.Time, 0).UTC()
		if !since.IsZero() && pubTime.Before(since) {
			continue
		}

		postURL := item.URL
		if postURL == "" {
			postURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", item.ID)
		}

		posts = append(posts, RawPost{
			ExternalID: strconv.FormatInt(item.ID, 10),
			URL:        postURL,
			Title:      item.Title,
			Author:     item.By,
			Body:       stripHTML(item.Text),
			PostedAt:   pubTime,
			Raw:        itemData,
		})
	}

	return posts, nil
}
