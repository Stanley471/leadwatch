package source

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RedditConfig holds parameters for the Reddit source.
type RedditConfig struct {
	ClientID     string
	ClientSecret string
	Subreddits   []string
}

// DefaultSubreddits returns the default subreddits for lead monitoring.
func DefaultSubreddits() []string {
	return []string{"forhire", "slavelabour", "webdev", "startups", "smallbusiness"}
}

// RedditSource polls Reddit using official OAuth (script application flow) when credentials
// are provided; otherwise it falls back to public RSS feeds for configured subreddits.
type RedditSource struct {
	client       *Client
	clientID     string
	clientSecret string
	subreddits   []string

	oauthBaseURL string
	tokenURL     string
	rssBaseURL   string

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

func NewRedditSource(client *Client, cfg RedditConfig) *RedditSource {
	if client == nil {
		client = NewClient(nil)
	}

	subs := cfg.Subreddits
	if len(subs) == 0 {
		subs = DefaultSubreddits()
	}

	return &RedditSource{
		client:       client,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		subreddits:   subs,
		oauthBaseURL: "https://oauth.reddit.com",
		tokenURL:     "https://www.reddit.com/api/v1/access_token",
		rssBaseURL:   "https://www.reddit.com",
	}
}

func (s *RedditSource) Name() string {
	return "reddit"
}

func (s *RedditSource) SetBaseURLs(oauthBase, tokenURL, rssBase string) {
	s.oauthBaseURL = oauthBase
	s.tokenURL = tokenURL
	s.rssBaseURL = rssBase
}

// HasOAuthCredentials returns true if client_id and client_secret are provided.
func (s *RedditSource) HasOAuthCredentials() bool {
	return strings.TrimSpace(s.clientID) != "" && strings.TrimSpace(s.clientSecret) != ""
}

type redditTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// ensureToken handles OAuth app-only authentication and automatic token refresh.
func (s *RedditSource) ensureToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If token exists and is valid for at least 1 more minute, reuse it
	if s.accessToken != "" && time.Now().Before(s.tokenExpiry.Add(-1*time.Minute)) {
		return s.accessToken, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.clientID, s.clientSecret)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reddit oauth token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("reddit oauth token error (status %d): %s", resp.StatusCode, string(body))
	}

	var tokResp redditTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return "", fmt.Errorf("reddit decode token response: %w", err)
	}

	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("reddit oauth returned empty access token")
	}

	s.accessToken = tokResp.AccessToken
	s.tokenExpiry = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)

	return s.accessToken, nil
}

type redditListingResponse struct {
	Data struct {
		Children []struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

type redditPostData struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Title     string  `json:"title"`
	Author    string  `json:"author"`
	Selftext  string  `json:"selftext"`
	Permalink string  `json:"permalink"`
	CreatedUTC float64 `json:"created_utc"`
	Subreddit string  `json:"subreddit"`
}

// Poll fetches new posts across configured subreddits.
// If OAuth credentials exist, it uses the official OAuth REST API (oauth.reddit.com).
// Otherwise, it falls back to subreddit RSS feeds (reddit.com/r/{sub}/.rss).
func (s *RedditSource) Poll(ctx context.Context, since time.Time) ([]RawPost, error) {
	if s.HasOAuthCredentials() {
		slog.Info("Polling Reddit using official OAuth API", "subreddits", s.subreddits)
		return s.pollOAuth(ctx, since)
	}

	slog.Info("Reddit client credentials not configured; falling back to public subreddit RSS feeds", "subreddits", s.subreddits)
	return s.pollRSS(ctx, since)
}

func (s *RedditSource) pollOAuth(ctx context.Context, since time.Time) ([]RawPost, error) {
	token, err := s.ensureToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticating reddit oauth: %w", err)
	}

	var allPosts []RawPost

	for _, sub := range s.subreddits {
		endpoint := fmt.Sprintf("%s/r/%s/new?limit=25", s.oauthBaseURL, url.PathEscape(sub))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := s.client.Do(req)
		if err != nil {
			slog.Error("Reddit OAuth listing request failed", "sub", sub, "err", err)
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			// Force token refresh on next attempt
			s.mu.Lock()
			s.accessToken = ""
			s.mu.Unlock()
			return nil, fmt.Errorf("reddit oauth token expired or unauthorized")
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			slog.Error("Reading Reddit response body failed", "sub", sub, "err", err)
			continue
		}

		var listing redditListingResponse
		if err := json.Unmarshal(bodyBytes, &listing); err != nil {
			slog.Error("Parsing Reddit JSON listing failed", "sub", sub, "err", err)
			continue
		}

		for _, child := range listing.Data.Children {
			var post redditPostData
			if err := json.Unmarshal(child.Data, &post); err != nil {
				continue
			}

			// Filter: For /r/forhire, keep only posts containing [Hiring] in the title
			if strings.EqualFold(sub, "forhire") && !strings.Contains(strings.ToLower(post.Title), "[hiring]") {
				continue
			}

			postedAt := time.Unix(int64(post.CreatedUTC), 0).UTC()
			if !since.IsZero() && postedAt.Before(since) {
				continue
			}

			postURL := post.Permalink
			if !strings.HasPrefix(postURL, "http") {
				postURL = "https://www.reddit.com" + postURL
			}

			allPosts = append(allPosts, RawPost{
				ExternalID: "reddit_" + post.ID,
				URL:        postURL,
				Author:     post.Author,
				Title:      post.Title,
				Body:       post.Selftext,
				PostedAt:   postedAt,
				Raw:        child.Data,
			})
		}
	}

	return allPosts, nil
}

type redditRSSFeed struct {
	Entries []redditRSSEntry `xml:"entry"`
}

type redditRSSEntry struct {
	ID        string    `xml:"id"`
	Title     string    `xml:"title"`
	Author    struct {
		Name string `xml:"name"`
		URI  string `xml:"uri"`
	} `xml:"author"`
	Content   string    `xml:"content"`
	Updated   time.Time `xml:"updated"`
	Link      struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

func (s *RedditSource) pollRSS(ctx context.Context, since time.Time) ([]RawPost, error) {
	var allPosts []RawPost

	for _, sub := range s.subreddits {
		feedURL := fmt.Sprintf("%s/r/%s/.rss", s.rssBaseURL, url.PathEscape(sub))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
		if err != nil {
			continue
		}

		resp, err := s.client.Do(req)
		if err != nil {
			slog.Error("Reddit RSS poll failed", "sub", sub, "err", err)
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		var feed redditRSSFeed
		if err := xml.Unmarshal(bodyBytes, &feed); err != nil {
			slog.Error("Reddit RSS XML unmarshal failed", "sub", sub, "err", err)
			continue
		}

		for _, entry := range feed.Entries {
			if strings.EqualFold(sub, "forhire") && !strings.Contains(strings.ToLower(entry.Title), "[hiring]") {
				continue
			}

			if !since.IsZero() && entry.Updated.Before(since) {
				continue
			}

			rawBytes, _ := json.Marshal(entry)

			allPosts = append(allPosts, RawPost{
				ExternalID: entry.ID,
				URL:        entry.Link.Href,
				Author:     strings.TrimPrefix(entry.Author.Name, "/u/"),
				Title:      entry.Title,
				Body:       stripHTML(entry.Content),
				PostedAt:   entry.Updated.UTC(),
				Raw:        rawBytes,
			})
		}
	}

	return allPosts, nil
}
