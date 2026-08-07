package prtemplate

import (
	"fmt"
	"regexp"
	"strings"
)

// Bounds on a pr: template, mirroring the review.path_instructions
// precedent (internal/config/config.go): validated at config *parse* time,
// because an oversized prompt fails the agent invocation outright rather
// than degrading, and that failure should happen before a run starts.
const (
	MaxPRSections        = 16
	MaxPRTemplateBytes   = 8192 // total template text across title + all sections
	MaxPRSlotsPerSection = 8
	MaxPRSlotBriefBytes  = 1024 // one {{ ... }} brief
	DefaultPRSlotChars   = 1200 // default max_chars for a body slot/section
	MaxPRTitleChars      = 200

	// MaxWorstCaseRenderedBytes is the fixed generous ceiling worst-case
	// rendered size is checked against at config load. The real per-provider
	// limit (e.g. Azure DevOps' 4000 chars) is unknown until the PR step, so
	// this is a config-time sanity ceiling, not the runtime shedding budget.
	MaxWorstCaseRenderedBytes = 63488
)

// TitleInput is the pre-decode-but-post-merge shape of pr.title, handed to
// Build by internal/config. Conventional and Strict are left as pointers so
// Build can apply the presence-dependent / fixed defaults (Decisions 3, 4)
// only when the caller has not set them explicitly.
type TitleInput struct {
	Template     string
	MaxChars     int
	Conventional *bool
	Strict       *bool
	MustMatch    string
}

// SectionInput is the pre-decode-but-post-merge shape of one pr.sections
// entry. Required has already been resolved from the YAML `required: true`
// key (Decision 1) to a bool by the caller (absent means false).
type SectionInput struct {
	ID       string
	Heading  string
	Required bool
	MaxChars int
	Content  string
	Source   string
}

// Build parses and validates a title template and section list into a ready
// -to-render Template, or returns the first validation error found. All
// checks are fail-closed and meant to run at config load (Decision 2 and the
// "Validation and bounds" bullets): an unknown source:, a duplicate id:, a
// section with both content: and source:, an unclosed "{{", an empty brief,
// a missing evidence floor, or an exceeded bound are all config errors here
// rather than a mid-run PR-step failure.
func Build(title TitleInput, sections []SectionInput) (*Template, error) {
	tmpl := &Template{}

	if title.MaxChars < 0 {
		return nil, fmt.Errorf("pr.title.max_chars must not be negative")
	}
	if title.MaxChars > MaxPRTitleChars {
		return nil, fmt.Errorf("pr.title.max_chars %d exceeds the maximum of %d", title.MaxChars, MaxPRTitleChars)
	}
	titleSegments, err := parseSegments(title.Template, "title")
	if err != nil {
		return nil, fmt.Errorf("pr.title.template: %w", err)
	}
	if err := validateSlotBounds("pr.title.template", titleSegments); err != nil {
		return nil, err
	}

	conventional := title.Template == ""
	if title.Conventional != nil {
		conventional = *title.Conventional
	}
	strict := true
	if title.Strict != nil {
		strict = *title.Strict
	}

	var mustMatch *MustMatch
	if raw := strings.TrimSpace(title.MustMatch); raw != "" {
		re, err := regexp.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("pr.title.must_match %q does not compile: %w", raw, err)
		}
		mustMatch = &MustMatch{Raw: raw, Pattern: re}
	}

	tmpl.Title = Title{
		Segments:     titleSegments,
		MaxChars:     title.MaxChars,
		Conventional: conventional,
		Strict:       strict,
		MustMatch:    mustMatch,
	}

	if len(sections) > MaxPRSections {
		return nil, fmt.Errorf("pr.sections has %d entries, at most %d are allowed", len(sections), MaxPRSections)
	}

	seenIDs := map[string]bool{}
	hasFloor := false
	totalTemplateBytes := len(title.Template)
	worstCase := 0

	built := make([]Section, 0, len(sections))
	for i, in := range sections {
		id := strings.TrimSpace(in.ID)
		if id != "" {
			if seenIDs[id] {
				return nil, fmt.Errorf("pr.sections[%d]: duplicate id %q", i, id)
			}
			seenIDs[id] = true
		}

		hasContent := strings.TrimSpace(in.Content) != ""
		source := SourceKind(strings.TrimSpace(in.Source))
		if hasContent && source != "" {
			return nil, fmt.Errorf("pr.sections[%d] (%q): a section must carry either content: or source:, never both", i, sectionLabel(id, in.Heading))
		}
		if !hasContent && source == "" {
			return nil, fmt.Errorf("pr.sections[%d] (%q): a section must carry either content: or source:", i, sectionLabel(id, in.Heading))
		}

		section := Section{
			ID:       id,
			Heading:  strings.TrimSpace(in.Heading),
			Required: in.Required,
			MaxChars: in.MaxChars,
		}
		if section.MaxChars < 0 {
			return nil, fmt.Errorf("pr.sections[%d] (%q): max_chars must not be negative", i, sectionLabel(id, in.Heading))
		}

		if source != "" {
			if !validSources[source] {
				return nil, fmt.Errorf("pr.sections[%d] (%q): unknown source %q", i, sectionLabel(id, in.Heading), source)
			}
			section.Source = source
			if IsFloorSource(source) {
				hasFloor = true
			}
		} else {
			segs, err := parseSegments(in.Content, sectionKeyPrefix(id, i))
			if err != nil {
				return nil, fmt.Errorf("pr.sections[%d] (%q): %w", i, sectionLabel(id, in.Heading), err)
			}
			if !hasSlots(segs) && !hasLiteralText(segs) {
				return nil, fmt.Errorf("pr.sections[%d] (%q): content has no literal text and no placeholders", i, sectionLabel(id, in.Heading))
			}
			if err := validateSlotBounds(fmt.Sprintf("pr.sections[%d] (%q)", i, sectionLabel(id, in.Heading)), segs); err != nil {
				return nil, err
			}
			section.Segments = segs
			totalTemplateBytes += len(in.Content)
		}

		built = append(built, section)
		worstCase += worstCaseSectionBytes(section)
	}

	// The floor is required only when a section list is actually configured
	// (Decision 2). A title-only template (e.g. must_match with no
	// pr.sections at all) has no body to guard: body composition stays the
	// legacy composer's, unmodified, and the floor it already renders.
	if len(sections) > 0 && !hasFloor {
		return nil, fmt.Errorf("pr.sections must include exactly one of `source: pipeline` or `source: pipeline.summary` (the evidence floor); none was found")
	}
	if totalTemplateBytes > MaxPRTemplateBytes {
		return nil, fmt.Errorf("pr: template text totals %d bytes across title and all sections, at most %d are allowed", totalTemplateBytes, MaxPRTemplateBytes)
	}

	worstCase += worstCaseTitleBytes(tmpl.Title)
	if worstCase > MaxWorstCaseRenderedBytes {
		return nil, fmt.Errorf("pr: worst-case rendered body is approximately %d bytes, at most %d are allowed; lower max_chars on one or more sections", worstCase, MaxWorstCaseRenderedBytes)
	}

	tmpl.Sections = built
	return tmpl, nil
}

func sectionLabel(id, heading string) string {
	if heading != "" {
		return heading
	}
	if id != "" {
		return id
	}
	return "unnamed section"
}

func sectionKeyPrefix(id string, index int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("section%d", index)
}

func validateSlotBounds(label string, segments []Segment) error {
	count := 0
	for _, seg := range segments {
		if seg.Slot == nil {
			continue
		}
		count++
		if len(seg.Slot.Brief) > MaxPRSlotBriefBytes {
			return fmt.Errorf("%s: placeholder brief %q is %d bytes, at most %d are allowed", label, truncateForError(seg.Slot.Brief), len(seg.Slot.Brief), MaxPRSlotBriefBytes)
		}
	}
	if count > MaxPRSlotsPerSection {
		return fmt.Errorf("%s: has %d placeholders, at most %d are allowed", label, count, MaxPRSlotsPerSection)
	}
	return nil
}

func truncateForError(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func worstCaseSectionBytes(s Section) int {
	if s.IsSourceSection() {
		// Deterministic blocks are bounded elsewhere (prsummary.go / testing
		// summary); charge a modest fixed allowance here so the config-time
		// ceiling stays meaningful without duplicating that accounting.
		return len(s.Heading) + DefaultPRSlotChars
	}
	maxChars := s.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultPRSlotChars
	}
	return len(s.Heading) + maxChars + len("\n\n## \n\n")
}

func worstCaseTitleBytes(t Title) int {
	if t.MaxChars > 0 {
		return t.MaxChars
	}
	return MaxPRTitleChars
}
