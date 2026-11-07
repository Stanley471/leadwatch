package classify

import (
	"context"
	"strings"

	"leadwatch/internal/store"
)

type Classifier struct{}

func New() *Classifier {
	return &Classifier{}
}

type ClassificationResult struct {
	IsHiring   bool
	Category   string
	Confidence float64
}

// Classify determines if a post is genuinely an employer or client seeking to hire a web developer.
func (c *Classifier) Classify(ctx context.Context, p *store.Post) ClassificationResult {
	text := strings.ToLower(p.Title + " " + p.Body)

	// Negative signals (someone looking for work themselves, tutorials, spam)
	seekerSignals := []string{
		"for hire",
		"[for hire]",
		"looking for work",
		"hire me",
		"seeking employment",
		"seeking opportunities",
		"available for freelance",
		"i am a web developer",
		"portfolio review",
	}

	for _, sig := range seekerSignals {
		if strings.Contains(text, sig) {
			return ClassificationResult{
				IsHiring:   false,
				Category:   "job_seeker",
				Confidence: 0.95,
			}
		}
	}

	// Positive hiring signals
	hiringSignals := []string{
		"hiring",
		"[hiring]",
		"looking for a developer",
		"looking for a web developer",
		"looking for an engineer",
		"we are looking for",
		"we are hiring",
		"need a developer",
		"need a web developer",
		"contractor needed",
		"freelancer needed",
		"job opening",
		"position:",
		"role:",
	}

	hiringCount := 0
	for _, sig := range hiringSignals {
		if strings.Contains(text, sig) {
			hiringCount++
		}
	}

	// Web dev keywords
	webKeywords := []string{
		"web developer", "full stack", "fullstack", "frontend", "front-end",
		"backend", "back-end", "react", "golang", "go developer", "next.js",
		"vue", "angular", "typescript", "javascript", "tailwind", "node.js",
		"html", "css", "api", "database",
	}

	webCount := 0
	for _, kw := range webKeywords {
		if strings.Contains(text, kw) {
			webCount++
		}
	}

	// Source-based prior (e.g. HN jobstories and remoteok are curated hiring feeds)
	isCuratedHiringSource := p.Source == "hackernews" || p.Source == "remoteok" || p.Source == "weworkremotely"

	isHiring := (hiringCount > 0 || isCuratedHiringSource) && webCount > 0
	category := "other"
	if webCount > 0 {
		category = "web_development"
	}

	confidence := 0.60
	if hiringCount >= 2 && webCount >= 2 {
		confidence = 0.95
	} else if hiringCount >= 1 && webCount >= 1 {
		confidence = 0.85
	} else if isCuratedHiringSource && webCount >= 1 {
		confidence = 0.80
	}

	return ClassificationResult{
		IsHiring:   isHiring,
		Category:   category,
		Confidence: confidence,
	}
}
