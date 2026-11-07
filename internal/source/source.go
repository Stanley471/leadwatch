package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const (
	DefaultUserAgent = "leadwatch/0.1"
	DefaultTimeout   = 25 * time.Second
	MaxRetries       = 3
	InitialBackoff   = 1 * time.Second
)

var tagRegex = regexp.MustCompile("<[^>]*>")

func stripHTML(input string) string {
	cleaned := tagRegex.ReplaceAllString(input, "")
	return html.UnescapeString(cleaned)
}

// RawPost is the raw un-normalized post from any public source.
type RawPost struct {
	ExternalID string
	URL        string
	Author     string
	Title      string
	Body       string
	PostedAt   time.Time
	Raw        json.RawMessage
}

// Source represents an external lead provider.
type Source interface {
	Name() string
	Poll(ctx context.Context, since time.Time) ([]RawPost, error)
}

// Client wraps http.Client with Generic User-Agent, Exponential Backoff, and Rate Limit header handling.
type Client struct {
	httpClient *http.Client
	userAgent  string
}

func NewClient(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{
			Timeout: DefaultTimeout,
		}
	}
	return &Client{
		httpClient: hc,
		userAgent:  DefaultUserAgent,
	}
}

func (c *Client) SetUserAgent(ua string) {
	c.userAgent = ua
}

// Do executes an HTTP request with exponential backoff on 429 and 5xx errors, respecting rate limits.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}

		resp, err = c.httpClient.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				retryAfterHeader := resp.Header.Get("Retry-After")
				resp.Body.Close()

				if attempt == MaxRetries {
					return nil, fmt.Errorf("server error %d after %d retries", resp.StatusCode, MaxRetries)
				}

				var sleepDuration time.Duration
				if retryAfterHeader != "" {
					if seconds, parseErr := strconv.Atoi(retryAfterHeader); parseErr == nil && seconds > 0 {
						sleepDuration = time.Duration(seconds) * time.Second
					}
				}

				if sleepDuration <= 0 {
					backoff := float64(InitialBackoff) * math.Pow(2, float64(attempt))
					jitter := rand.Float64() * 0.3 * backoff
					sleepDuration = time.Duration(backoff + jitter)
				}

				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(sleepDuration):
					continue
				}
			} else {
				return resp, nil
			}
		} else {
			if attempt == MaxRetries {
				return nil, err
			}
		}

		backoff := float64(InitialBackoff) * math.Pow(2, float64(attempt))
		jitter := rand.Float64() * 0.3 * backoff
		sleepDuration := time.Duration(backoff + jitter)

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(sleepDuration):
		}
	}

	if err != nil {
		return nil, err
	}
	return nil, errors.New("request failed with maximum retries exceeded")
}

func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
