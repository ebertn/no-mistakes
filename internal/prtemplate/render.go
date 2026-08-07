package prtemplate

import "strings"

// declineMarkers are answers that are not answers (mechanism 2 / the prompt
// contract): the agent is told never to write these, and rendering treats
// them exactly like null if one slips through anyway.
var declineMarkers = map[string]bool{
	"n/a":            true,
	"none":           true,
	"tbd":            true,
	"see above":      true,
	"unknown":        true,
	"not applicable": true,
}

// punctuationGlueChars are the characters a purely-punctuation literal
// segment may consist of. Such a segment drops when it ends up adjacent to
// the start of the string, the end of the string, or another dropped
// segment - the elision rule's step 2.
const punctuationGlueChars = ": -|/,()[]{}#> \t"

// IsUnresolved reports whether a raw slot value (nil or agent-returned
// string) counts as unresolved: null, empty after trimming, or one of the
// decline-markers the agent was told never to use but might anyway.
func IsUnresolved(raw *string) bool {
	return normalizeSlotValue(raw) == ""
}

func normalizeSlotValue(raw *string) string {
	if raw == nil {
		return ""
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return ""
	}
	if declineMarkers[strings.ToLower(v)] {
		return ""
	}
	return v
}

func isPunctuationOnlyLiteral(s string) bool {
	if strings.TrimSpace(s) == "" {
		return s != ""
	}
	for _, r := range s {
		if !strings.ContainsRune(punctuationGlueChars, r) {
			return false
		}
	}
	return true
}

// demoteHeadings prevents a slot value from forging a markdown section
// break: any line whose (possibly indented) leading `#{1,3}` looks like a
// heading is demoted to `####`.
func demoteHeadings(v string) string {
	lines := strings.Split(v, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		lead := len(line) - len(trimmed)
		hashes := 0
		for hashes < len(trimmed) && hashes < 3 && trimmed[hashes] == '#' {
			hashes++
		}
		if hashes == 0 {
			continue
		}
		rest := trimmed[hashes:]
		if rest == "" || strings.HasPrefix(rest, " ") {
			lines[i] = line[:lead] + "####" + rest
		}
	}
	return strings.Join(lines, "\n")
}

// RenderSegments renders literal+slot segments into text per the elision
// rule: drop every unresolved slot, drop punctuation-only literal glue left
// adjacent to a dropped segment or an edge, then collapse horizontal
// whitespace runs and trim. Newlines are preserved so multi-paragraph body
// content is untouched; the collapsing step exists for title-style glue
// (": ", " | ") left dangling by an elided neighbor, not for prose.
func RenderSegments(segments []Segment, values map[string]*string) string {
	resolved := make([]bool, len(segments))
	texts := make([]string, len(segments))
	for i, seg := range segments {
		if seg.Slot == nil {
			texts[i] = seg.Literal
			continue
		}
		v := normalizeSlotValue(values[seg.Slot.Key])
		if v == "" {
			continue
		}
		texts[i] = demoteHeadings(v)
		resolved[i] = true
	}

	dropped := make([]bool, len(segments))
	for i, seg := range segments {
		if seg.Slot != nil {
			dropped[i] = !resolved[i]
		}
	}
	for changed := true; changed; {
		changed = false
		for i, seg := range segments {
			if seg.Slot != nil || dropped[i] {
				continue
			}
			if !isPunctuationOnlyLiteral(seg.Literal) {
				continue
			}
			leftEdge := i == 0 || dropped[i-1]
			rightEdge := i == len(segments)-1 || dropped[i+1]
			if leftEdge || rightEdge {
				dropped[i] = true
				changed = true
			}
		}
	}

	var b strings.Builder
	for i, seg := range segments {
		if dropped[i] || seg.Slot != nil && !resolved[i] {
			continue
		}
		b.WriteString(texts[i])
	}
	return collapseHorizontalWhitespace(b.String())
}

func collapseHorizontalWhitespace(s string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if lastSpace {
				continue
			}
			lastSpace = true
			b.WriteRune(' ')
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return trimLines(b.String())
}

// trimLines trims trailing horizontal whitespace from each line and the
// leading/trailing whitespace of the whole string, without touching
// intentional blank lines between paragraphs.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// UnresolvedSlotKeys returns the keys, in segment order, of every slot in
// segments whose value is unresolved.
func UnresolvedSlotKeys(segments []Segment, values map[string]*string) []string {
	var keys []string
	for _, seg := range segments {
		if seg.Slot == nil {
			continue
		}
		if IsUnresolved(values[seg.Slot.Key]) {
			keys = append(keys, seg.Slot.Key)
		}
	}
	return keys
}

// TruncateAtLineBoundary truncates text to at most maxChars runes, cutting at
// the last newline before the limit when one exists, and appends marker
// (preceded by a blank line) when truncation occurred. Mirrors the contract
// of the existing pr.go truncateTextAtLineBoundary helper for this package's
// own bounded content.
func TruncateAtLineBoundary(text string, maxChars int, marker string) string {
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	suffix := ""
	if marker != "" {
		suffix = "\n\n" + marker
	}
	available := maxChars - len([]rune(suffix))
	if available <= 0 {
		if len([]rune(suffix)) <= maxChars {
			return strings.TrimLeft(suffix, "\n")
		}
		return ""
	}
	head := string(runes[:available])
	if cut := strings.LastIndex(head, "\n"); cut > 0 {
		head = head[:cut]
	}
	return strings.TrimRight(head, "\n") + suffix
}

func sectionTruncationMarker() string {
	return "_... (section truncated to stay within its configured max_chars.)_"
}

// RenderedSection pairs a Section with its fully rendered markdown block
// (heading included). An empty Markdown means the section has nothing to
// say and drops entirely, heading included.
type RenderedSection struct {
	Section  Section
	Markdown string
}

// RenderContentSection renders a content: section's heading + body given
// resolved slot values. Returns "" when the section has nothing to say (the
// caller must drop it, heading included) - this is what makes an optional
// section with an unresolved placeholder take its heading with it.
func RenderContentSection(section Section, values map[string]*string) string {
	body := RenderSegments(section.Segments, values)
	if strings.TrimSpace(body) == "" {
		return ""
	}
	if section.MaxChars > 0 {
		body = TruncateAtLineBoundary(body, section.MaxChars, sectionTruncationMarker())
	}
	heading := section.Heading
	if heading == "" {
		heading = "Section"
	}
	return "## " + heading + "\n\n" + body
}

// AssembleBody joins rendered, non-empty section blocks in declared order
// within bodyLimit (0 = unlimited, measured in runes). Over budget: optional
// (non-required, non-floor) blocks shed one at a time in reverse declaration
// order; if still over, required non-floor blocks truncate at a line
// boundary starting from the last one; the floor block is never shed or
// truncated.
func AssembleBody(blocks []RenderedSection, bodyLimit int) string {
	present := nonEmptyRenderedSections(blocks)
	full := joinRenderedSections(present)
	if bodyLimit <= 0 || len([]rune(full)) <= bodyLimit {
		return full
	}

	working := append([]RenderedSection{}, present...)
	for i := len(working) - 1; i >= 0; i-- {
		if working[i].Section.IsFloor() || working[i].Section.Required {
			continue
		}
		candidate := removeRenderedSectionAt(working, i)
		if len([]rune(joinRenderedSections(candidate))) <= bodyLimit {
			return joinRenderedSections(candidate)
		}
		working = candidate
	}

	for len([]rune(joinRenderedSections(working))) > bodyLimit {
		idx := lastNonFloorIndex(working)
		if idx < 0 {
			break
		}
		others := removeRenderedSectionAt(working, idx)
		othersRendered := joinRenderedSections(others)
		sep := "\n\n"
		if othersRendered == "" {
			sep = ""
		}
		budget := bodyLimit - len([]rune(othersRendered)) - len([]rune(sep))
		if budget <= 0 {
			working = others
			continue
		}
		truncated := TruncateAtLineBoundary(working[idx].Markdown, budget, sectionTruncationMarker())
		if truncated == "" || len([]rune(truncated)) >= len([]rune(working[idx].Markdown)) {
			working = others
			continue
		}
		working[idx].Markdown = truncated
	}
	return joinRenderedSections(working)
}

func lastNonFloorIndex(list []RenderedSection) int {
	for i := len(list) - 1; i >= 0; i-- {
		if !list[i].Section.IsFloor() {
			return i
		}
	}
	return -1
}

func nonEmptyRenderedSections(list []RenderedSection) []RenderedSection {
	out := make([]RenderedSection, 0, len(list))
	for _, b := range list {
		if strings.TrimSpace(b.Markdown) != "" {
			out = append(out, b)
		}
	}
	return out
}

func joinRenderedSections(list []RenderedSection) string {
	parts := make([]string, 0, len(list))
	for _, b := range list {
		parts = append(parts, b.Markdown)
	}
	return strings.Join(parts, "\n\n")
}

func removeRenderedSectionAt(list []RenderedSection, i int) []RenderedSection {
	out := make([]RenderedSection, 0, len(list)-1)
	out = append(out, list[:i]...)
	out = append(out, list[i+1:]...)
	return out
}
