package server

import (
	"errors"
	"fmt"
	"strings"

	"marketplace/internal/skillvalidation"
)

// maxSkillDescriptionChars is the Claude Code SDK's limit on the `description`
// field in SKILL.md frontmatter. A skill whose description exceeds it is
// dropped from the marketplace sync — the plugin still syncs, the skill
// silently does not — so we reject it at every write path rather than let it
// reach the catalog and go missing there. Shared with the AI validator's
// rubric so the enforced limit and the reviewed limit cannot drift apart.
const maxSkillDescriptionChars = skillvalidation.MaxDescriptionChars

// descriptionLen counts UTF-16 code units, matching what the SDK's
// JavaScript-side check counts and what String.length reports in the editor.
// Counting runes instead would let an emoji-bearing description pass here
// and still be rejected downstream.
func descriptionLen(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++ // astral plane: encoded as a surrogate pair
		}
	}
	return n
}

// validateSkillDescription enforces presence and length. The returned error
// text is safe to hand straight back to the caller.
func validateSkillDescription(desc string) error {
	if strings.TrimSpace(desc) == "" {
		return errors.New("description is required")
	}
	if n := descriptionLen(desc); n > maxSkillDescriptionChars {
		return fmt.Errorf("description must be at most %d characters (got %d)", maxSkillDescriptionChars, n)
	}
	return nil
}
