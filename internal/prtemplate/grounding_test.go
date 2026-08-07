package prtemplate

import "testing"

func TestGrounding_IdentifierNotInContextIsRejected(t *testing.T) {
	if IsGrounded("APF-9999", "branch: feature/x", "commit: unrelated change", "intent: do the thing") {
		t.Fatal("expected an identifier absent from context to be ungrounded")
	}
}

func TestGrounding_IdentifierInBranchName_InCommitMessage_InIntent(t *testing.T) {
	cases := []struct {
		name    string
		context []string
	}{
		{"branch", []string{"feature/APF-2531-fix-thing", "", ""}},
		{"commit", []string{"", "fix: correct thing\n\nRefs: APF-2531", ""}},
		{"intent", []string{"", "", "the goal is to resolve APF-2531"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !IsGrounded("APF-2531", tc.context...) {
				t.Fatalf("expected APF-2531 to be grounded via %s", tc.name)
			}
		})
	}
}

func TestGrounding_CaseInsensitiveMatch(t *testing.T) {
	if !IsGrounded("apf-2531", "branch contains APF-2531") {
		t.Fatal("expected case-insensitive match")
	}
}

func TestGrounding_ProseIsNeverChecked(t *testing.T) {
	prose := "Unify targeting rule limits and add search-first value selection"
	if !IsGrounded(prose) {
		t.Fatal("prose must never be grounding-checked (no context needed to pass)")
	}
	multiline := "line one\nline two mentions APF-9999 nowhere else"
	if !IsGrounded(multiline) {
		t.Fatal("multi-line prose must never be grounding-checked")
	}
}

func TestGrounding_ShapeHeuristicRequiresADigit(t *testing.T) {
	if IsIdentifierShaped("Refactor") {
		t.Fatal("a bare word with no digit must not be identifier-shaped")
	}
	if !IsIdentifierShaped("APF-2531") {
		t.Fatal("APF-2531 must be identifier-shaped")
	}
	if IsIdentifierShaped("has spaces 2531") {
		t.Fatal("a value with internal whitespace must not be identifier-shaped")
	}
	if IsIdentifierShaped("") {
		t.Fatal("empty string must not be identifier-shaped")
	}
}
