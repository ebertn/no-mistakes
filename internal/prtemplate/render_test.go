package prtemplate

import (
	"strings"
	"testing"
)

func strptr(s string) *string { return &s }

func TestRender_UnresolvedTitleSlotElidesItsPunctuationGlue(t *testing.T) {
	segments, err := parseSegments("{{ ticket }}: {{ title }}", "title")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}

	both := RenderSegments(segments, map[string]*string{
		"title_1": strptr("APF-2531"),
		"title_2": strptr("Unify targeting rule limits"),
	})
	if both != "APF-2531: Unify targeting rule limits" {
		t.Fatalf("both resolved = %q", both)
	}

	noTicket := RenderSegments(segments, map[string]*string{
		"title_1": nil,
		"title_2": strptr("Unify targeting rule limits"),
	})
	if noTicket != "Unify targeting rule limits" {
		t.Fatalf("no ticket = %q, want no leading \": \"", noTicket)
	}
	if strings.HasPrefix(noTicket, ":") {
		t.Fatalf("no ticket = %q must not start with dangling punctuation", noTicket)
	}
}

func TestRender_PunctuationBetweenTwoResolvedSlotsIsKept(t *testing.T) {
	segments, err := parseSegments("{{ a }}: {{ b }}", "title")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	got := RenderSegments(segments, map[string]*string{
		"title_1": strptr("A"),
		"title_2": strptr("B"),
	})
	if got != "A: B" {
		t.Fatalf("got %q, want %q", got, "A: B")
	}
}

func TestRender_NoCodePathEmitsALiteralPlaceholder(t *testing.T) {
	segments, err := parseSegments("{{ ticket }}: {{ title }}", "title")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	cases := []map[string]*string{
		{"title_1": nil, "title_2": strptr("x")},
		{"title_1": strptr(""), "title_2": strptr("x")},
		{"title_1": strptr("   "), "title_2": strptr("x")},
		{"title_1": strptr("N/A"), "title_2": strptr("x")},
		{"title_1": strptr("none"), "title_2": strptr("x")},
		{"title_1": strptr("TBD"), "title_2": strptr("x")},
		{"title_1": strptr("See above"), "title_2": strptr("x")},
		{"title_1": strptr("unknown"), "title_2": strptr("x")},
	}
	for _, values := range cases {
		got := RenderSegments(segments, values)
		if strings.Contains(got, "{{") {
			t.Fatalf("rendered %q contains a literal placeholder for input %v", got, values)
		}
	}
}

func TestRender_DeclineMarkersTreatedAsUnresolved(t *testing.T) {
	for _, marker := range []string{"N/A", "None", "TBD", "See above", "unknown", "Not Applicable"} {
		if !IsUnresolved(strptr(marker)) {
			t.Fatalf("decline marker %q should be treated as unresolved", marker)
		}
	}
	if IsUnresolved(strptr("a real value")) {
		t.Fatal("a real prose value must not be treated as unresolved")
	}
}

func TestRender_OptionalSectionDropsHeadingWhenEmpty(t *testing.T) {
	section := Section{
		ID:       "sister_prs",
		Heading:  "Sister PRs",
		Required: false,
		Segments: mustParse(t, "{{ sister prs }}", "sister_prs"),
	}
	got := RenderContentSection(section, map[string]*string{"sister_prs_1": nil})
	if got != "" {
		t.Fatalf("expected empty render for unresolved optional section, got %q", got)
	}
}

func TestRender_SlotHeadingsAreDemoted(t *testing.T) {
	section := Section{
		ID:       "s",
		Heading:  "Section",
		Segments: mustParse(t, "{{ x }}", "s"),
	}
	got := RenderContentSection(section, map[string]*string{"s_1": strptr("## Injected Heading\nbody")})
	if strings.Contains(got, "\n## Injected Heading") {
		t.Fatalf("slot value forged a section break: %q", got)
	}
	if !strings.Contains(got, "#### Injected Heading") {
		t.Fatalf("expected demoted heading, got %q", got)
	}
}

func TestRender_MaxCharsTruncatesAtLineBoundaryWithMarker(t *testing.T) {
	const maxChars = 150
	section := Section{
		ID:       "s",
		Heading:  "Section",
		MaxChars: maxChars,
		Segments: mustParse(t, "{{ x }}", "s"),
	}
	long := strings.Repeat("line of text\n", 20)
	got := RenderContentSection(section, map[string]*string{"s_1": strptr(long)})
	if len([]rune(got)) > maxChars+len([]rune(sectionTruncationMarker()))+len("## Section\n\n")+10 {
		t.Fatalf("rendered section not bounded: %d runes: %q", len([]rune(got)), got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}

func TestRender_FloorIsNeverShedOrTruncated(t *testing.T) {
	floor := RenderedSection{
		Section:  Section{ID: "pipeline", Source: SourcePipelineSummary},
		Markdown: "## Pipeline\n\nUpdates from git push no-mistakes\n\n<!-- attestation -->",
	}
	optional := RenderedSection{
		Section:  Section{ID: "extra", Required: false},
		Markdown: "## Extra\n\n" + strings.Repeat("filler ", 200),
	}
	got := AssembleBody([]RenderedSection{optional, floor}, len([]rune(floor.Markdown))+5)
	if !strings.Contains(got, "attestation") {
		t.Fatalf("floor was dropped: %q", got)
	}
	if strings.Contains(got, "## Extra") {
		t.Fatalf("expected optional section to be shed, got %q", got)
	}
}

func TestRender_ShedsOptionalSectionsInReverseDeclarationOrder(t *testing.T) {
	floor := RenderedSection{Section: Section{ID: "pipeline", Source: SourcePipelineSummary}, Markdown: "## Pipeline\n\nfloor"}
	first := RenderedSection{Section: Section{ID: "first"}, Markdown: "## First\n\nfirst body"}
	second := RenderedSection{Section: Section{ID: "second"}, Markdown: "## Second\n\nsecond body"}

	full := joinRenderedSections([]RenderedSection{first, second, floor})
	// A budget that fits everything except the last optional section.
	budget := len([]rune(full)) - len([]rune("\n\n"+second.Markdown))
	got := AssembleBody([]RenderedSection{first, second, floor}, budget)
	if strings.Contains(got, "## Second") {
		t.Fatalf("expected the LAST-declared optional section to shed first, got %q", got)
	}
	if !strings.Contains(got, "## First") {
		t.Fatalf("expected the earlier-declared optional section to survive, got %q", got)
	}
}

func TestRender_RequiredSectionTruncatesInsteadOfShedding(t *testing.T) {
	floor := RenderedSection{Section: Section{ID: "pipeline", Source: SourcePipelineSummary}, Markdown: "## Pipeline\n\nfloor"}
	required := RenderedSection{
		Section:  Section{ID: "required", Required: true},
		Markdown: "## Required\n\n" + strings.Repeat("x", 500),
	}
	const budget = 200
	got := AssembleBody([]RenderedSection{required, floor}, budget)
	if !strings.Contains(got, "## Required") {
		t.Fatalf("required section must survive (truncated), got %q", got)
	}
	if !strings.Contains(got, "floor") {
		t.Fatalf("floor must survive, got %q", got)
	}
	if len([]rune(got)) > budget+len([]rune(sectionTruncationMarker()))+40 {
		t.Fatalf("assembled body not bounded: %d runes: %q", len([]rune(got)), got)
	}
}

func mustParse(t *testing.T, text, prefix string) []Segment {
	t.Helper()
	segs, err := parseSegments(text, prefix)
	if err != nil {
		t.Fatalf("parseSegments(%q): %v", text, err)
	}
	return segs
}
