package score

import (
	"context"
	"fmt"
	"strings"

	"leadwatch/internal/config"
	"leadwatch/internal/store"
)

type Scorer struct {
	bio config.DeveloperBio
}

func New(bio config.DeveloperBio) *Scorer {
	return &Scorer{bio: bio}
}

type ScoreResult struct {
	Score  int
	Reason string
}

// Score computes match score (0-100) based on developer skills, project requirements, budget, etc.
func (s *Scorer) Score(ctx context.Context, p *store.Post) ScoreResult {
	if !p.IsHiring {
		return ScoreResult{Score: 0, Reason: "not hiring"}
	}

	text := strings.ToLower(p.Title + " " + p.Body)
	totalScore := 20 // baseline for valid hiring lead
	reasons := []string{"baseline(+20)"}

	// 1. Skill Matches (up to +50 points)
	matchedSkills := 0
	for _, skill := range s.bio.Skills {
		lowerSkill := strings.ToLower(skill)
		if strings.Contains(text, lowerSkill) {
			matchedSkills++
			totalScore += 10
			if totalScore > 70 {
				totalScore = 70
			}
		}
	}
	if matchedSkills > 0 {
		reasons = append(reasons, fmt.Sprintf("skills_matched:%d", matchedSkills))
	}

	// 2. High-value terms (+15 points)
	highValueTerms := []string{"contract", "freelance", "remote", "immediate", "urgent", "budget", "$"}
	highValueCount := 0
	for _, term := range highValueTerms {
		if strings.Contains(text, term) {
			highValueCount++
		}
	}
	if highValueCount > 0 {
		bonus := highValueCount * 5
		if bonus > 15 {
			bonus = 15
		}
		totalScore += bonus
		reasons = append(reasons, fmt.Sprintf("high_value_terms(+%d)", bonus))
	}

	// 3. Negative / Unfavorable filters (-20 points)
	lowValueTerms := []string{"unpaid", "equity only", "intern", "junior only", "no budget"}
	for _, term := range lowValueTerms {
		if strings.Contains(text, term) {
			totalScore -= 20
			reasons = append(reasons, fmt.Sprintf("penalty:%s(-20)", term))
		}
	}

	// 4. Confidence scaling
	if p.Confidence > 0 {
		scaled := float64(totalScore) * p.Confidence
		totalScore = int(scaled)
	}

	if totalScore < 0 {
		totalScore = 0
	}
	if totalScore > 100 {
		totalScore = 100
	}

	return ScoreResult{
		Score:  totalScore,
		Reason: strings.Join(reasons, ", "),
	}
}
