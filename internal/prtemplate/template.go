// Package prtemplate implements the parsing, validation, JSON-schema
// derivation, rendering, and groundedness checking for the configurable PR
// title/description template (`pr:` config namespace). It has no dependency
// on internal/config so that package can depend on this one without a cycle.
package prtemplate

import (
	"fmt"
	"strings"
)

// SourceKind names a deterministic, pipeline-generated body block. It is a
// closed set: these are code-generated facts, not prose, unlike the
// open-ended natural-language content a section may carry instead.
type SourceKind string

const (
	SourceRunIntent       SourceKind = "run.intent"
	SourcePipelineRisk    SourceKind = "pipeline.risk"
	SourcePipelineTesting SourceKind = "pipeline.testing"
	SourcePipelineSummary SourceKind = "pipeline.summary"
	SourcePipeline        SourceKind = "pipeline"
)

// validSources is the complete source: vocabulary a section may reference.
var validSources = map[SourceKind]bool{
	SourceRunIntent:       true,
	SourcePipelineRisk:    true,
	SourcePipelineTesting: true,
	SourcePipelineSummary: true,
	SourcePipeline:        true,
}

// IsFloorSource reports whether source is one of the two evidence-floor
// sources (pipeline or pipeline.summary), which a template may never omit or
// have elided/dropped at render time.
func IsFloorSource(source SourceKind) bool {
	return source == SourcePipeline || source == SourcePipelineSummary
}

// Slot is one `{{ brief }}` placeholder. Key is the stable JSON property name
// the agent's structured output must address; Brief is the natural-language
// instruction text between the braces, verbatim (trimmed).
type Slot struct {
	Key   string
	Brief string
}

// Segment is one piece of a parsed template: either literal text (Slot ==
// nil) or a placeholder (Slot != nil, Literal == "").
type Segment struct {
	Literal string
	Slot    *Slot
}

// Title is the parsed and resolved `pr.title` policy.
type Title struct {
	Segments []Segment
	MaxChars int // 0 = unbounded

	// Conventional, Strict, and MustMatch are the resolved (not raw) policy:
	// Merge/Resolve has already applied the presence-dependent default for
	// Conventional (Decision 3) and the fixed true default for Strict
	// (Decision 4).
	Conventional bool
	Strict       bool
	MustMatch    *MustMatch
}

// MustMatch is a compiled, optional title-format guard (Decision 9).
type MustMatch struct {
	Raw     string
	Pattern matcher
}

// matcher is the minimal regexp surface prtemplate needs, so tests can supply
// a fake without pulling in regexp semantics they don't exercise.
type matcher interface {
	MatchString(string) bool
}

// Section is one entry in `pr.sections`. Exactly one of Segments (a
// content: template) or Source (a source: reference) is set.
type Section struct {
	ID       string
	Heading  string
	Required bool
	MaxChars int // 0 = unbounded
	Segments []Segment
	Source   SourceKind
}

// IsFloor reports whether this section is one of the two non-omittable
// evidence-floor sources.
func (s Section) IsFloor() bool { return IsFloorSource(s.Source) }

// IsSourceSection reports whether this section renders a deterministic
// pipeline-owned block rather than agent-filled content.
func (s Section) IsSourceSection() bool { return s.Source != "" }

// Template is a fully parsed and resolved `pr:` template, ready for schema
// derivation and rendering. Sections may be empty: a title-only template
// (only pr.title.* configured, no pr.sections) is legal, and the caller must
// then keep the legacy body composer and apply only the title policy
// (Conventional/Strict/MustMatch) on top of the agent's own drafted title.
type Template struct {
	Title    Title
	Sections []Section
}

// HasSections reports whether this template configures a section list at
// all. When false, only the title policy is in effect and body composition
// stays the legacy composer's.
func (t *Template) HasSections() bool { return len(t.Sections) > 0 }

// Slots returns every slot across the title and all content sections, in
// template order (title first). Source sections contribute none.
func (t *Template) Slots() []*Slot {
	var out []*Slot
	for i := range t.Title.Segments {
		if s := t.Title.Segments[i].Slot; s != nil {
			out = append(out, s)
		}
	}
	for _, section := range t.Sections {
		for i := range section.Segments {
			if s := section.Segments[i].Slot; s != nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// parseSegments lexes a `{{ brief }}` template string into alternating
// literal and slot segments, in order. keyPrefix names the slots
// "<keyPrefix>_1", "<keyPrefix>_2", ... in the order they appear. Rejects an
// unclosed "{{" and a brief that is empty or all whitespace once trimmed.
func parseSegments(text, keyPrefix string) ([]Segment, error) {
	var segments []Segment
	rest := text
	n := 0
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			if rest != "" {
				segments = append(segments, Segment{Literal: rest})
			}
			break
		}
		if start > 0 {
			segments = append(segments, Segment{Literal: rest[:start]})
		}
		afterOpen := rest[start+len("{{"):]
		end := strings.Index(afterOpen, "}}")
		if end < 0 {
			return nil, fmt.Errorf("unclosed \"{{\" in template %q", text)
		}
		brief := strings.TrimSpace(afterOpen[:end])
		if brief == "" {
			return nil, fmt.Errorf("empty {{ }} placeholder in template %q", text)
		}
		n++
		segments = append(segments, Segment{Slot: &Slot{
			Key:   fmt.Sprintf("%s_%d", keyPrefix, n),
			Brief: brief,
		}})
		rest = afterOpen[end+len("}}"):]
	}
	return segments, nil
}

// hasSlots reports whether segments contains at least one slot.
func hasSlots(segments []Segment) bool {
	for _, s := range segments {
		if s.Slot != nil {
			return true
		}
	}
	return false
}

// hasLiteralText reports whether segments contains any non-empty literal
// text (whitespace-only literals do not count).
func hasLiteralText(segments []Segment) bool {
	for _, s := range segments {
		if s.Slot == nil && strings.TrimSpace(s.Literal) != "" {
			return true
		}
	}
	return false
}
