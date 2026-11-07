package source

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// aTagRegex extracts href and anchor text from <a href="...">text</a>
	aTagRegex = regexp.MustCompile(`(?i)<a\s+[^>]*href=["']([^"']*)["'][^>]*>(.*?)</a>`)
	pTagRegex = regexp.MustCompile(`(?i)<p>`)
)

// htmlToPlainText converts HTML comments to plain text while keeping link URLs.
func htmlToPlainText(input string) string {
	if input == "" {
		return ""
	}

	// 1. Convert <a href="url">text</a> -> text (url) or just url if text is url
	replacedLinks := aTagRegex.ReplaceAllStringFunc(input, func(m string) string {
		submatches := aTagRegex.FindStringSubmatch(m)
		if len(submatches) >= 3 {
			href := strings.TrimSpace(submatches[1])
			inner := strings.TrimSpace(tagRegex.ReplaceAllString(submatches[2], ""))
			if inner == "" || inner == href {
				return href
			}
			return fmt.Sprintf("%s (%s)", inner, href)
		}
		return m
	})

	// 2. Replace <p> with newlines
	withNewlines := pTagRegex.ReplaceAllString(replacedLinks, "\n\n")

	// 3. Strip remaining tags
	stripped := tagRegex.ReplaceAllString(withNewlines, "")

	// 4. Unescape HTML entities
	unescaped := html.UnescapeString(stripped)

	return strings.TrimSpace(unescaped)
}

// HNFreelancerSource polls Hacker News for the monthly "Ask HN: Freelancer? Seeking freelancer?" thread
// using the Algolia Search API and extracts top-level comments.
type HNFreelancerSource struct {
	client  *Client
	baseURL string

	mu           sync.Mutex
	cachedMonth  string
	cachedStoryID string
}

func NewHNFreelancerSource(client *Client) *HNFreelancerSource {
	if client == nil {
		client = NewClient(nil)
	}
	return &HNFreelancerSource{
		client:  client,
		baseURL: "https://hn.algolia.com/api/v1",
	}
}

func (s *HNFreelancerSource) SetBaseURL(u string) {
	s.baseURL = u
}

func (s *HNFreelancerSource) Name() string {
	return "hackernews_freelancer"
}

type algoliaSearchResponse struct {
	Hits []algoliaHit `json:"hits"`
}

type algoliaHit struct {
	ObjectID    string `json:"objectID"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	CommentText string `json:"comment_text"`
	StoryID     int64  `json:"story_id"`
	ParentID    int64  `json:"parent_id"`
	CreatedAt   string `json:"created_at"`
	CreatedAtI  int64  `json:"created_at_i"`
}

// findMonthlyThread finds the latest "Freelancer? Seeking freelancer?" story for the current month.
func (s *HNFreelancerSource) findMonthlyThread(ctx context.Context, now time.Time) (string, error) {
	s.mu.Lock()
	currentMonthKey := now.Format("2006-01")
	if s.cachedMonth == currentMonthKey && s.cachedStoryID != "" {
		id := s.cachedStoryID
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()

	// Query Algolia for the monthly thread: "Freelancer? Seeking freelancer?"
	// Ask HN monthly threads usually start on the 1st of each month.
	query := "Freelancer? Seeking freelancer?"
	reqURL := fmt.Sprintf("%s/search_by_date?query=%s&tags=ask_hn&hitsPerPage=10",
		s.baseURL,
		url.QueryEscape(query),
	)

	data, err := s.client.Get(ctx, reqURL)
	if err != nil {
		return "", fmt.Errorf("search hn thread: %w", err)
	}

	var searchResp algoliaSearchResponse
	if err := json.Unmarshal(data, &searchResp); err != nil {
		return "", fmt.Errorf("parse search response: %w", err)
	}

	targetMonthName := now.Format("January 2006")
	var foundID string

	for _, hit := range searchResp.Hits {
		titleLower := strings.ToLower(hit.Title)
		if strings.Contains(titleLower, "freelancer? seeking freelancer?") {
			// Check if it belongs to this month
			hitTime := time.Unix(hit.CreatedAtI, 0).UTC()
			if hitTime.Format("2006-01") == currentMonthKey || strings.Contains(titleLower, strings.ToLower(targetMonthName)) {
				foundID = hit.ObjectID
				break
			}
			// If not yet posted for current month, we keep the most recent one if posted within the last 35 days
			if foundID == "" && now.Sub(hitTime) < 35*24*time.Hour {
				foundID = hit.ObjectID
			}
		}
	}

	if foundID != "" {
		s.mu.Lock()
		s.cachedMonth = currentMonthKey
		s.cachedStoryID = foundID
		s.mu.Unlock()
		return foundID, nil
	}

	return "", nil
}

// Poll fetches top-level comments under the monthly freelancer thread newer than `since`.
func (s *HNFreelancerSource) Poll(ctx context.Context, since time.Time) ([]RawPost, error) {
	now := time.Now().UTC()
	storyID, err := s.findMonthlyThread(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("finding monthly freelancer thread: %w", err)
	}

	if storyID == "" {
		slog.Info("No HN monthly freelancer thread found yet this month")
		return nil, nil
	}

	storyIDNum, _ := strconv.ParseInt(storyID, 10, 64)

	// Fetch comments for this story
	var filterParam string
	if !since.IsZero() {
		filterParam = fmt.Sprintf("&numericFilters=created_at_i>%d", since.Unix())
	}

	commentsURL := fmt.Sprintf("%s/search_by_date?tags=comment,story_%s&hitsPerPage=200%s",
		s.baseURL,
		storyID,
		filterParam,
	)

	data, err := s.client.Get(ctx, commentsURL)
	if err != nil {
		return nil, fmt.Errorf("fetching comments for story %s: %w", storyID, err)
	}

	var commentsResp algoliaSearchResponse
	if err := json.Unmarshal(data, &commentsResp); err != nil {
		return nil, fmt.Errorf("parsing comments: %w", err)
	}

	var posts []RawPost
	for _, hit := range commentsResp.Hits {
		// Only consider top-level comments (where parent_id == story_id)
		if storyIDNum != 0 && hit.ParentID != 0 && hit.ParentID != storyIDNum {
			continue
		}

		postedAt := time.Unix(hit.CreatedAtI, 0).UTC()
		if !since.IsZero() && postedAt.Before(since) {
			continue
		}

		plainText := htmlToPlainText(hit.CommentText)
		if strings.TrimSpace(plainText) == "" {
			continue
		}

		// First line as title
		firstLine := plainText
		if idx := strings.Index(firstLine, "\n"); idx != -1 {
			firstLine = strings.TrimSpace(firstLine[:idx])
		}
		if len(firstLine) > 120 {
			firstLine = firstLine[:117] + "..."
		}

		commentURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%s", hit.ObjectID)
		rawBytes, _ := json.Marshal(hit)

		posts = append(posts, RawPost{
			ExternalID: "hn_" + hit.ObjectID,
			URL:        commentURL,
			Author:     hit.Author,
			Title:      firstLine,
			Body:       plainText,
			PostedAt:   postedAt,
			Raw:        rawBytes,
		})
	}

	return posts, nil
}
