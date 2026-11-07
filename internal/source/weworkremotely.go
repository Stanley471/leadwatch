package source

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

type WWRSource struct {
	client  *Client
	feedURL string
}

func NewWWRSource(client *Client) *WWRSource {
	if client == nil {
		client = NewClient(nil)
	}
	return &WWRSource{
		client:  client,
		feedURL: "https://weworkremotely.com/categories/remote-front-end-programming-jobs.rss",
	}
}

func (s *WWRSource) SetFeedURL(u string) {
	s.feedURL = u
}

func (s *WWRSource) Name() string {
	return "weworkremotely"
}

type rssChannel struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

func (s *WWRSource) Poll(ctx context.Context, since time.Time) ([]RawPost, error) {
	data, err := s.client.Get(ctx, s.feedURL)
	if err != nil {
		return nil, fmt.Errorf("wwr poll: %w", err)
	}

	var channel rssChannel
	if err := xml.Unmarshal(data, &channel); err != nil {
		return nil, fmt.Errorf("wwr rss parse: %w", err)
	}

	var posts []RawPost
	for _, item := range channel.Items {
		guid := item.GUID
		if guid == "" {
			guid = item.Link
		}
		if guid == "" {
			continue
		}

		pubTime, err := time.Parse(time.RFC1123Z, item.PubDate)
		if err != nil {
			pubTime, err = time.Parse(time.RFC1123, item.PubDate)
			if err != nil {
				pubTime = time.Now().UTC()
			}
		}

		if !since.IsZero() && pubTime.Before(since) {
			continue
		}

		posts = append(posts, RawPost{
			Source:      s.Name(),
			ExternalID:  guid,
			URL:         item.Link,
			Title:       item.Title,
			Author:      "WeWorkRemotely",
			Body:        stripHTML(item.Description),
			PublishedAt: pubTime,
		})
	}

	return posts, nil
}
