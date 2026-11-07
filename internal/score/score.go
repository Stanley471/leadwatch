package score

import (
	"context"
	"fmt"
	"strings"

	"leadwatch/internal/store"
)

type DeveloperBio struct {
	Name      string
	Title     string
	Skills    []string
	Portfolio string
	MinScore  int
}

type Scorer struct {
	bio DeveloperBio
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

func New(bio DeveloperBio) *Scorer {
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

	lowValueTerms := []string{"unpaid", "equity only", "intern", "junior only", "no budget"}
	for _, term := range lowValueTerms {
		if strings.Contains(text, term) {
			totalScore -= 20
			reasons = append(reasons, fmt.Sprintf("penalty:%s(-20)", term))
		}
	}

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
