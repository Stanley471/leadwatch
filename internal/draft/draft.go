package draft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"leadwatch/internal/store"
)

// Drafter prepares tailored, high-converting 4-6 sentence proposals.
// NEVER sends messages automatically.
type Drafter struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

func New(apiKey, model string, hc *http.Client) *Drafter {
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	if hc == nil {
		hc = &http.Client{
			Timeout: 45 * time.Second,
		}
	}
	return &Drafter{
		apiKey:     apiKey,
		model:      model,
		baseURL:    "https://api.anthropic.com/v1/messages",
		httpClient: hc,
	}
}

func (d *Drafter) SetBaseURL(u string) {
	d.baseURL = u
}

// FindBestPortfolioMatch selects the portfolio item with maximum tag overlap against post text and classification tech.
func FindBestPortfolioMatch(items []store.PortfolioItem, post *store.Post, classRec *store.ClassificationRecord) *store.PortfolioItem {
	if len(items) == 0 {
		return nil
	}

	searchScope := strings.ToLower(post.Title + " " + post.Body)
	if classRec != nil {
		searchScope += " " + strings.ToLower(classRec.Tech) + " " + strings.ToLower(classRec.ProjectType)
	}

	var bestItem *store.PortfolioItem
	maxMatches := -1

	for i := range items {
		it := &items[i]
		tags := strings.Split(it.Tags, ",")
		matches := 0
		for _, tag := range tags {
			tClean := strings.TrimSpace(strings.ToLower(tag))
			if tClean != "" && strings.Contains(searchScope, tClean) {
				matches++
			}
		}

		if matches > maxMatches {
			maxMatches = matches
			bestItem = it
		}
	}

	return bestItem
}

// GuessChannel determines the natural communication channel from the post's source and text.
func GuessChannel(post *store.Post) string {
	s := strings.ToLower(post.Source)
	text := strings.ToLower(post.Body)

	if strings.Contains(s, "reddit") {
		if strings.Contains(text, "email") || strings.Contains(text, "@") {
			return "email"
		}
		if strings.Contains(text, "chat") || strings.Contains(text, "dm") {
			return "reddit_dm"
		}
		return "reddit_pm"
	}

	if strings.Contains(s, "hn") || strings.Contains(s, "hackernews") {
		if strings.Contains(text, "email") || strings.Contains(text, "@") {
			return "email"
		}
		return "email"
	}

	if strings.Contains(s, "remoteok") || strings.Contains(s, "weworkremotely") {
		return "application_url"
	}

	return "email"
}

type anthropicReq struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateDraft produces a personalized 4-6 sentence proposal referencing ONLY real portfolio items and facts.
func (d *Drafter) GenerateDraft(
	ctx context.Context,
	post *store.Post,
	classRec *store.ClassificationRecord,
	profile *store.ProfileConfig,
	portfolio *store.PortfolioItem,
) (string, error) {
	// Fallback to high-quality template if Anthropic API key is not configured
	if d.apiKey == "" {
		return GenerateTemplateDraft(post, classRec, profile, portfolio), nil
	}

	portfolioCtx := "No specific portfolio item selected."
	if portfolio != nil {
		portfolioCtx = fmt.Sprintf("Title: %s\nURL: %s\nDescription: %s", portfolio.Title, portfolio.URL, portfolio.Description)
	}

	tone := "professional, friendly, and concise"
	if profile != nil && profile.Tone != "" {
		tone = profile.Tone
	}

	systemInstruction := fmt.Sprintf(`You are an expert freelance proposal writer.
Write a personalized 4-6 sentence reply for the developer to send to the client.

STRICT RULES:
1. Length: Exactly 4 to 6 sentences.
2. Structure:
   - Sentence 1: Concisely restate their specific need or goal.
   - Sentence 2: Reference the relevant similar project from the provided portfolio (include its link if available). NEVER invent or claim projects that do not exist.
   - Sentence 3-4: Offer one concrete technical suggestion or thoughtful clarifying question based on their stack/scope.
   - Sentence 5-6: Propose a clear, low-friction next step (e.g. quick 10-minute intro call or brief message chat).
3. Tone: %s.
4. Output: Output ONLY the plain text proposal without explanations, metadata, or markdown tags.`, tone)

	userPrompt := fmt.Sprintf(`Client Post:
Title: %s
Author: %s
Body: %s

Classification Info:
Project Type: %s
Tech: %s

Available Portfolio Item:
%s`,
		post.Title,
		post.Author,
		post.Body,
		func() string {
			if classRec != nil {
				return classRec.ProjectType
			}
			return "web development"
		}(),
		func() string {
			if classRec != nil {
				return classRec.Tech
			}
			return "web technologies"
		}(),
		portfolioCtx,
	)

	payload := anthropicReq{
		Model:     d.model,
		MaxTokens: 400,
		System:    systemInstruction,
		Messages: []anthropicMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	reqBody, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", d.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		slog.Warn("Drafter API request failed, falling back to template draft", "err", err)
		return GenerateTemplateDraft(post, classRec, profile, portfolio), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		slog.Warn("Drafter API non-200, falling back to template draft", "status", resp.StatusCode, "body", string(respBytes))
		return GenerateTemplateDraft(post, classRec, profile, portfolio), nil
	}

	var aResp anthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&aResp); err != nil || len(aResp.Content) == 0 {
		return GenerateTemplateDraft(post, classRec, profile, portfolio), nil
	}

	reply := strings.TrimSpace(aResp.Content[0].Text)
	if reply == "" {
		return GenerateTemplateDraft(post, classRec, profile, portfolio), nil
	}

	return reply, nil
}

// GenerateTemplateDraft creates an authentic deterministic 4-6 sentence proposal without external LLM calls.
func GenerateTemplateDraft(
	post *store.Post,
	classRec *store.ClassificationRecord,
	profile *store.ProfileConfig,
	portfolio *store.PortfolioItem,
) string {
	greeting := "Hi"
	if post.Author != "" && !strings.Contains(strings.ToLower(post.Author), "bot") {
		greeting = fmt.Sprintf("Hi %s", post.Author)
	}

	projectType := "web application"
	if classRec != nil && classRec.ProjectType != "" {
		projectType = strings.ReplaceAll(classRec.ProjectType, "_", " ")
	}

	// 1. Restate need
	s1 := fmt.Sprintf("%s, I saw your post regarding your need for a %s and would love to help you build this.", greeting, projectType)

	// 2. Relevant similar project
	s2 := ""
	if portfolio != nil {
		s2 = fmt.Sprintf("I recently built %s (%s), which solved a very similar set of technical requirements.", portfolio.Title, portfolio.URL)
	} else {
		s2 = "I specialize in building performant, production-ready full-stack applications with modern web tooling."
	}

	// 3. Concrete suggestion
	s3 := "Based on your scope, I recommend structuring the architecture with a modular backend API and a fast, responsive UI to keep initial delivery fast and scalable."

	// 4. Concrete question
	s4 := "Do you already have Figma designs prepared, or would you like assistance with the UX layout and schema design as well?"

	// 5. Clear next step
	s5 := "When would be a convenient time for a brief 10-minute chat or message exchange to review details and timeline?"

	return fmt.Sprintf("%s %s %s %s %s", s1, s2, s3, s4, s5)
}

// ProcessDraftingBatch finds scored leads, generates proposals with portfolio matching, and persists to outreach table.
func ProcessDraftingBatch(ctx context.Context, st *store.Store, drafter *Drafter, limit int) (int, error) {
	posts, err := st.GetPostsByStatus(ctx, store.StatusScored, limit)
	if err != nil {
		return 0, err
	}

	portfolioItems, _ := st.ListPortfolioItems(ctx)
	profileSettings, _ := st.GetProfileSettings(ctx)

	draftedCount := 0
	for _, p := range posts {
		if ctx.Err() != nil {
			return draftedCount, ctx.Err()
		}

		classRec, _ := st.GetDetailedClassification(ctx, p.ID)
		bestPortfolio := FindBestPortfolioMatch(portfolioItems, p, classRec)

		draftText, err := drafter.GenerateDraft(ctx, p, classRec, profileSettings, bestPortfolio)
		if err != nil {
			slog.Error("failed generating draft for post", "id", p.ID, "err", err)
			continue
		}

		channel := GuessChannel(p)
		now := time.Now().UTC()

		// 1. Save to outreach table
		if err := st.SaveOutreachDraft(ctx, p.ID, channel, draftText); err != nil {
			slog.Error("failed saving outreach record", "id", p.ID, "err", err)
			continue
		}

		// 2. Update post status to drafted
		if err := st.SaveDraft(ctx, p.ID, draftText, now, store.StatusDrafted); err != nil {
			slog.Error("failed updating post to drafted", "id", p.ID, "err", err)
			continue
		}

		draftedCount++
	}

	return draftedCount, nil
}

// Backwards compatibility
func (d *Drafter) Draft(ctx context.Context, p *store.Post) string {
	return GenerateTemplateDraft(p, nil, nil, nil)
}
