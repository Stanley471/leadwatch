package score

import (
	"fmt"
	"math"
	"strings"
	"time"

	"leadwatch/internal/store"
)

// Weights configures the scoring balance across signals.
type Weights struct {
	MaxRecencyPoints     float64
	MaxBudgetPoints      float64
	MaxSpecificityPoints float64
	MaxFitPoints         float64

	ScamPenaltyWeight     float64
	ConfidencePenaltyMult float64
}

// DefaultWeights provides safe, well-tuned scoring defaults.
func DefaultWeights() Weights {
	return Weights{
		MaxRecencyPoints:      25.0,
		MaxBudgetPoints:       25.0,
		MaxSpecificityPoints:  25.0,
		MaxFitPoints:          25.0,
		ScamPenaltyWeight:     1.0,  // subtract up to 100 points
		ConfidencePenaltyMult: 0.25, // subtract up to 25 points if confidence is low
	}
}

// Profile defines the developer's criteria, rate requirements, and services offered.
type Profile struct {
	RateFloor float64  // Minimum expected budget / project rate floor in USD
	Services  []string // e.g. "landing_site", "web_app", "ecommerce", "maintenance", "Go", "React"
	Locations []string // Allowed locations or preferences, e.g. "remote", "us", "worldwide"
}

// DefaultProfile returns sensible standard developer settings.
func DefaultProfile() Profile {
	return Profile{
		RateFloor: 500.0,
		Services:  []string{"web_app", "ecommerce", "landing_site", "maintenance", "go", "react", "typescript", "fullstack"},
		Locations: []string{"remote", "worldwide"},
	}
}

type ScoreResult struct {
	Score  int
	Reason string
}

// CalculateScore is a pure function that evaluates a post and its classification
// against the developer's profile and weights, returning a score from 0-100.
// Only classifications with is_request=true produce a positive lead score.
func CalculateScore(
	post *store.Post,
	classification *store.ClassificationRecord,
	profile Profile,
	weights Weights,
	now time.Time,
) ScoreResult {
	if classification == nil || !classification.IsRequest {
		return ScoreResult{Score: 0, Reason: "not a hiring request (is_request=false)"}
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	var score float64
	var reasons []string

	// 1. Recency Score (Steep decay after 2 hours)
	age := now.Sub(post.PublishedAt)
	if age < 0 {
		age = 0
	}

	var recencyScore float64
	if age <= 2*time.Hour {
		// Linear decrease during first 2 hours
		recencyScore = weights.MaxRecencyPoints * (1.0 - (age.Hours() / 4.0)) // 100% to 50%
	} else {
		// Steep exponential decay after 2 hours
		hoursPast := age.Hours() - 2.0
		halfLifeHours := 6.0
		decay := math.Exp(-math.Ln2 * hoursPast / halfLifeHours)
		recencyScore = (weights.MaxRecencyPoints * 0.5) * decay
	}
	if recencyScore < 0 {
		recencyScore = 0
	}
	score += recencyScore
	reasons = append(reasons, fmt.Sprintf("recency(%.1f/%.1f, age:%.1fh)", recencyScore, weights.MaxRecencyPoints, age.Hours()))

	// 2. Budget vs Profile Rate Floor
	var budgetScore float64
	if classification.BudgetAmount != nil && *classification.BudgetAmount > 0 {
		amt := *classification.BudgetAmount
		if amt >= profile.RateFloor {
			// At or above rate floor: full points plus ratio bonus
			ratio := amt / profile.RateFloor
			if ratio > 2.0 {
				ratio = 2.0
			}
			budgetScore = weights.MaxBudgetPoints * (0.8 + 0.2*(ratio-1.0))
		} else {
			// Below rate floor: scaled penalty
			ratio := amt / profile.RateFloor
			budgetScore = weights.MaxBudgetPoints * 0.5 * ratio
		}
	} else {
		// Budget not specified - neutral baseline (50% of budget points)
		budgetScore = weights.MaxBudgetPoints * 0.5
	}
	score += budgetScore
	reasons = append(reasons, fmt.Sprintf("budget(%.1f/%.1f)", budgetScore, weights.MaxBudgetPoints))

	// 3. Specificity Score (Text length + requirements words)
	fullText := strings.ToLower(post.Title + " " + post.Body)
	words := strings.Fields(fullText)
	lengthScore := 0.0
	if len(words) >= 80 {
		lengthScore = weights.MaxSpecificityPoints * 0.5
	} else {
		lengthScore = (float64(len(words)) / 80.0) * (weights.MaxSpecificityPoints * 0.5)
	}

	requirementKeywords := []string{
		"requirements", "stack", "experience", "scope", "timeline", "deliverables",
		"api", "database", "design", "figma", "responsibilities", "skills",
	}
	matchedReqs := 0
	for _, kw := range requirementKeywords {
		if strings.Contains(fullText, kw) {
			matchedReqs++
		}
	}
	reqRatio := float64(matchedReqs) / 4.0
	if reqRatio > 1.0 {
		reqRatio = 1.0
	}
	reqScore := (weights.MaxSpecificityPoints * 0.5) * reqRatio

	specificityScore := lengthScore + reqScore
	score += specificityScore
	reasons = append(reasons, fmt.Sprintf("specificity(%.1f/%.1f, words:%d, reqs:%d)", specificityScore, weights.MaxSpecificityPoints, len(words), matchedReqs))

	// 4. Fit with Services & Location
	var fitScore float64
	serviceMatches := 0
	projectType := strings.ToLower(classification.ProjectType)
	techList := strings.ToLower(classification.Tech)

	for _, s := range profile.Services {
		sLower := strings.ToLower(s)
		if sLower == projectType || strings.Contains(techList, sLower) || strings.Contains(fullText, sLower) {
			serviceMatches++
		}
	}
	serviceRatio := float64(serviceMatches) / 3.0
	if serviceRatio > 1.0 {
		serviceRatio = 1.0
	}

	locationBonus := 0.0
	if classification.RemoteOK == nil || *classification.RemoteOK {
		locationBonus = weights.MaxFitPoints * 0.4
	}
	fitScore = (weights.MaxFitPoints * 0.6 * serviceRatio) + locationBonus
	score += fitScore
	reasons = append(reasons, fmt.Sprintf("fit(%.1f/%.1f, matches:%d)", fitScore, weights.MaxFitPoints, serviceMatches))

	// 5. Deductions: Minus Scam Score
	scamPenalty := float64(classification.ScamScore) * weights.ScamPenaltyWeight
	if scamPenalty > 0 {
		score -= scamPenalty
		reasons = append(reasons, fmt.Sprintf("scam_penalty(-%.1f)", scamPenalty))
	}

	// 6. Deductions: Minus Confidence Penalty
	confidencePenalty := 0.0
	if classification.Confidence < 100 {
		confidenceDeficit := float64(100 - classification.Confidence)
		confidencePenalty = confidenceDeficit * weights.ConfidencePenaltyMult
		score -= confidencePenalty
		reasons = append(reasons, fmt.Sprintf("confidence_penalty(-%.1f)", confidencePenalty))
	}

	finalScore := int(math.Round(score))
	if finalScore < 0 {
		finalScore = 0
	}
	if finalScore > 100 {
		finalScore = 100
	}

	return ScoreResult{
		Score:  finalScore,
		Reason: strings.Join(reasons, ", "),
	}
}

// DeveloperBio kept for backwards compatibility.
type DeveloperBio struct {
	Name      string
	Title     string
	Skills    []string
	Portfolio string
	MinScore  int
}

func DefaultBio() DeveloperBio {
	return DeveloperBio{
		Name:      "Leadwatch User",
		Title:     "Full-stack Web Developer",
		Skills:    []string{"Go", "React", "TypeScript", "Node.js", "PostgreSQL", "Next.js", "TailwindCSS"},
		Portfolio: "https://example.com",
		MinScore:  50,
	}
}

type Scorer struct {
	profile Profile
	weights Weights
}

func New(profile Profile) *Scorer {
	return &Scorer{
		profile: profile,
		weights: DefaultWeights(),
	}
}

func (s *Scorer) ScorePost(post *store.Post, classification *store.ClassificationRecord, now time.Time) ScoreResult {
	return CalculateScore(post, classification, s.profile, s.weights, now)
}
