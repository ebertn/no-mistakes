package prtemplate

import (
	"regexp"
	"strings"
)

var identifierShapeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/#-]*$`)

// IsIdentifierShaped reports whether v looks like an identifier (a ticket
// key, PR number, commit SHA, service name) rather than prose: no internal
// whitespace, length <= 40, matching the identifier character class, and
// containing at least one digit. This is the shape gate that decides
// whether the grounding check applies at all - prose is never checked.
func IsIdentifierShaped(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 40 {
		return false
	}
	if !identifierShapeRe.MatchString(v) {
		return false
	}
	for _, r := range v {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// IsGrounded reports whether an identifier-shaped value appears verbatim
// (case-insensitively) somewhere in context (the branch name, commit
// messages, and intent text). A non-identifier-shaped (prose) value is
// always grounded: the check is deliberately narrow and never touches
// multi-word or multi-line answers.
func IsGrounded(value string, context ...string) bool {
	if !IsIdentifierShaped(value) {
		return true
	}
	needle := strings.ToLower(value)
	for _, c := range context {
		if strings.Contains(strings.ToLower(c), needle) {
			return true
		}
	}
	return false
}
