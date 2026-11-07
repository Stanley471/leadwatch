package source

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"time"
)

const (
	DefaultUserAgent = "leadwatch/0.1"
	DefaultTimeout   = 25 * time.Second
	MaxRetries       = 3
	InitialBackoff   = 1 * time.Second
)

// RawPost is the raw un-normalized post from any public source.
type RawPost struct {
	Source      string
	ExternalID  string
	URL         string
	Title       string
	Author      string
	Body        string
	PublishedAt time.Time
}

// Source represents an external lead provider.
type Source interface {
	Name() string
	Poll(ctx context.Context, since time.Time) ([]RawPost, error)
}

// Client wraps http.Client with Generic User-Agent and Exponential Backoff.
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

// Do executes an HTTP request with exponential backoff on 429 and 5xx errors.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= MaxRetries; attempt++ {
		// Context check before starting
		if err := req.Context().Err(); err != nil {
			return nil, err
		}

		resp, err = c.httpClient.Do(req)
		if err == nil {
			// Check if we should retry
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				resp.Body.Close()
				if attempt == MaxRetries {
					return nil, fmt.Errorf("server error %d after %d retries", resp.StatusCode, MaxRetries)
				}
			} else {
				return resp, nil
			}
		} else {
			if attempt == MaxRetries {
				return nil, err
			}
		}

		// Exponential backoff with jitter
		backoff := float64(InitialBackoff) * math.Pow(2, float64(attempt))
		jitter := rand.Float64() * 0.3 * backoff
		sleepDuration := time.Duration(backoff + jitter)

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(sleepDuration):
		}
	}

	return resp, err
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
