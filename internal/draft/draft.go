package draft

import (
	"context"
	"fmt"
	"strings"

	"leadwatch/internal/config"
	"leadwatch/internal/store"
)

type Drafter struct {
	bio config.DeveloperBio
}

func New(bio config.DeveloperBio) *Drafter {
	return &Drafter{bio: bio}
}

// Draft prepares a personalized, tailored proposal response for review.
// It NEVER sends messages automatically.
func (d *Drafter) Draft(ctx context.Context, p *store.Post) string {
	text := strings.ToLower(p.Title + " " + p.Body)

	// Identify relevant skills to highlight
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
