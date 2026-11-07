package draft

import (
	"context"
	"fmt"
	"strings"

	"leadwatch/internal/score"
	"leadwatch/internal/store"
)

type Drafter struct {
	bio score.DeveloperBio
}

func New(bio score.DeveloperBio) *Drafter {
	return &Drafter{bio: bio}
}

// Draft prepares a personalized proposal response for human review.
// It NEVER sends messages automatically.
func (d *Drafter) Draft(ctx context.Context, p *store.Post) string {
	text := strings.ToLower(p.Title + " " + p.Body)

	var highlighted []string
	for _, skill := range d.bio.Skills {
		if strings.Contains(text, strings.ToLower(skill)) {
			highlighted = append(highlighted, skill)
		}
	}
	if len(highlighted) == 0 {
		highlighted = d.bio.Skills
		if len(highlighted) > 3 {
			highlighted = highlighted[:3]
		}
	}

	skillsList := strings.Join(highlighted, ", ")

	greeting := "Hi,"
	if p.Author != "" && !strings.Contains(strings.ToLower(p.Author), "bot") {
		greeting = fmt.Sprintf("Hi %s,", p.Author)
	}

	draft := fmt.Sprintf(`%s

I saw your post regarding "%s" and wanted to reach out.

I am a %s specializing in %s. I have extensive experience building scalable web applications and delivering robust solutions on time.

You can view some of my recent work and background here:
%s

I would love to learn more about what you're building and discuss how I can help. When would be a good time for a quick chat?

Best regards,
%s`,
		greeting,
		p.Title,
		d.bio.Title,
		skillsList,
		d.bio.Portfolio,
		d.bio.Name,
	)

	return strings.TrimSpace(draft)
}
