package prtemplate

import (
	"strings"
	"testing"
)

func TestParse_SlotsAndLiteralsKeepOrder(t *testing.T) {
	segments, err := parseSegments("{{ ticket }}: {{ title }}", "title")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d: %+v", len(segments), segments)
	}
	if segments[0].Slot == nil || segments[0].Slot.Key != "title_1" || segments[0].Slot.Brief != "ticket" {
		t.Fatalf("segment 0 = %+v", segments[0])
	}
	if segments[1].Slot != nil || segments[1].Literal != ": " {
		t.Fatalf("segment 1 = %+v", segments[1])
	}
	if segments[2].Slot == nil || segments[2].Slot.Key != "title_2" || segments[2].Slot.Brief != "title" {
		t.Fatalf("segment 2 = %+v", segments[2])
	}
}

func TestParse_NoSlotsIsAllLiteral(t *testing.T) {
	segments, err := parseSegments("plain text, no placeholders", "title")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if len(segments) != 1 || segments[0].Slot != nil || segments[0].Literal != "plain text, no placeholders" {
		t.Fatalf("segments = %+v", segments)
	}
}

func TestParse_RejectsUnclosedDelimiter(t *testing.T) {
	if _, err := parseSegments("{{ ticket", "title"); err == nil {
		t.Fatal("expected error for unclosed {{")
	}
}

func TestParse_RejectsEmptyBrief(t *testing.T) {
	for _, in := range []string{"{{}}", "{{   }}", "{{\n}}"} {
		if _, err := parseSegments(in, "title"); err == nil {
			t.Fatalf("expected error for empty brief %q", in)
		}
	}
}

func TestParse_TrimsBriefWhitespace(t *testing.T) {
	segments, err := parseSegments("{{  the ticket id  }}", "title")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if segments[0].Slot.Brief != "the ticket id" {
		t.Fatalf("brief = %q", segments[0].Slot.Brief)
	}
}

// literalOf concatenates every literal segment's text, in order, so a test
// can assert on the fully rendered literal text without caring how parsing
// happened to split it across segments.
func literalOf(segments []Segment) string {
	var b strings.Builder
	for _, s := range segments {
		if s.Slot == nil {
			b.WriteString(s.Literal)
		}
	}
	return b.String()
}

func TestParse_EscapedOpenerIsLiteralAndOpensNoPlaceholder(t *testing.T) {
	segments, err := parseSegments(`Escaped \{{ backslash attempt }} stays literal`, "s")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if hasSlots(segments) {
		t.Fatalf("expected no slots, got %+v", segments)
	}
	got := literalOf(segments)
	want := `Escaped {{ backslash attempt }} stays literal`
	if got != want {
		t.Fatalf("literal text = %q, want %q", got, want)
	}
}

// TestParse_EscapeDocumentingCommitFixMessageCreatesNoPhantomSlot is the
// concrete motivating case from the escape-hatch decision: a template that
// documents another {{ }} convention (commit.fix_message's {{.Step}}) must
// not silently grow a phantom placeholder for it.
func TestParse_EscapeDocumentingCommitFixMessageCreatesNoPhantomSlot(t *testing.T) {
	segments, err := parseSegments(`Set fix_message to "no-mistakes(\{{.Step}}): x" then {{ summary }}`, "s")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	slots := 0
	for _, s := range segments {
		if s.Slot != nil {
			slots++
		}
	}
	if slots != 1 {
		t.Fatalf("expected exactly 1 real slot (the trailing {{ summary }}), got %d: %+v", slots, segments)
	}
	if !strings.Contains(literalOf(segments), `no-mistakes({{.Step}}): x`) {
		t.Fatalf("literal text = %q, want the escaped {{.Step}} preserved verbatim", literalOf(segments))
	}
}

// TestParse_DoubleBackslashPrecedesARealPlaceholder is the parity case: an
// escaped backslash (\\) followed by "{{" is a literal backslash plus a REAL
// placeholder, not a second escape - exactly like a shell or regex escaping
// its own metacharacter.
func TestParse_DoubleBackslashPrecedesARealPlaceholder(t *testing.T) {
	segments, err := parseSegments(`a\\{{ real }}`, "s")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if !hasSlots(segments) {
		t.Fatalf("expected a real placeholder after the even backslash run, got %+v", segments)
	}
	if literalOf(segments) != `a\\` {
		t.Fatalf("literal text = %q, want the backslash run published verbatim", literalOf(segments))
	}
	var brief string
	for _, s := range segments {
		if s.Slot != nil {
			brief = s.Slot.Brief
		}
	}
	if brief != "real" {
		t.Fatalf("brief = %q, want %q", brief, "real")
	}
}

func TestParse_UnrelatedBackslashIsOrdinaryLiteralText(t *testing.T) {
	segments, err := parseSegments(`a Windows path C:\Users\x then {{ y }}`, "s")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if !hasSlots(segments) {
		t.Fatalf("expected the unescaped {{ y }} to still open a placeholder, got %+v", segments)
	}
	if !strings.Contains(literalOf(segments), `C:\Users\x`) {
		t.Fatalf("literal text = %q, want the path backslashes untouched", literalOf(segments))
	}
}

func TestParse_EscapedOpenerFollowedByRealUnclosedStillErrors(t *testing.T) {
	if _, err := parseSegments(`\{{ escaped, then a real {{ unclosed`, "s"); err == nil {
		t.Fatal("expected an unclosed-\"{{\" error for the real, unescaped opener")
	}
}

func TestParse_TrailingLoneBackslashIsLiteral(t *testing.T) {
	segments, err := parseSegments(`trailing backslash \`, "s")
	if err != nil {
		t.Fatalf("parseSegments: %v", err)
	}
	if literalOf(segments) != `trailing backslash \` {
		t.Fatalf("literal text = %q", literalOf(segments))
	}
}
