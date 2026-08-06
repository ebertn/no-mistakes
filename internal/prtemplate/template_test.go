package prtemplate

import "testing"

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
