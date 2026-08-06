package config

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/prtemplate"
	"gopkg.in/yaml.v3"
)

// PR on_agent_failure values (Decision 8): fallback is the default, keeping
// today's behavior when the drafting agent produces no usable answer at
// all; fail parks the run instead of publishing a body that ignores the
// configured template.
const (
	PROnAgentFailureFallback = "fallback"
	PROnAgentFailureFail     = "fail"
)

// PRTitleRaw is the YAML representation of pr.title.
type PRTitleRaw struct {
	// Template is a natural-language title specification: literal text plus
	// {{ brief }} placeholders the agent fills. Empty means no title
	// template is configured (Decision 3: the template is the title spec,
	// there is no title convention of this design's own).
	Template string `yaml:"template"`
	MaxChars int    `yaml:"max_chars"`
	// Conventional gates the existing conventional.TightenTitle over the
	// FINAL rendered title. Defaults to false whenever Template is set (a
	// ticket-prefixed template should not get "chore: " prepended), true
	// when it is not (today's exact behavior).
	Conventional *bool `yaml:"conventional"`
	// Strict defaults true (Decision 4): an unresolved required title slot
	// fails the PR step after one re-ask rather than eliding.
	Strict *bool `yaml:"strict"`
	// MustMatch is an optional regex the FINAL published title must satisfy
	// (Decision 9), checked after substitution, elision, conventional, and
	// truncation.
	MustMatch string `yaml:"must_match"`
}

// PRSectionRaw is the YAML representation of one pr.sections entry. Exactly
// one of Content or Source must be set.
type PRSectionRaw struct {
	ID      string `yaml:"id"`
	Heading string `yaml:"heading"`
	// Required is a structured YAML key (Decision 1), not an inline
	// backtick attribute. Absent means false (optional): an unresolved
	// section elides, heading included, rather than failing the run.
	Required *bool  `yaml:"required"`
	MaxChars int    `yaml:"max_chars"`
	Content  string `yaml:"content"`
	Source   string `yaml:"source"`
}

// PRRaw is the YAML representation of the pr: config namespace, present on
// both GlobalConfig and RepoConfig. The whole namespace is trusted-only
// (Decision 6): EffectiveRepoConfig sources it exclusively from the
// default-branch copy, regardless of allow_repo_commands.
type PRRaw struct {
	Title    PRTitleRaw     `yaml:"title"`
	Sections []PRSectionRaw `yaml:"sections"`
	// Labels and Draft are PR metadata (Decision 7), applied at PR creation
	// only; they reach the forge rather than the body.
	Labels []string `yaml:"labels"`
	Draft  *bool    `yaml:"draft"`
	// OnAgentFailure is fallback (default) or fail (Decision 8).
	OnAgentFailure string `yaml:"on_agent_failure"`
	// PipelineSummary is the upstream #601 compatibility alias: when no
	// explicit Template/Sections are configured, pipeline_summary: false
	// means "keep the signature and attestation, drop only the <details>
	// narrative" - the bug-fixed version of #601's flag, which on upstream
	// main silently strips the #670 attestation too.
	PipelineSummary *bool `yaml:"pipeline_summary"`
}

// prRawAllowedKeys, prTitleAllowedKeys, and prSectionAllowedKeys are the
// closed key sets for the pr: namespace. Repo config parsing (unlike global
// config's LoadGlobal) does not decode with yaml.Decoder.KnownFields(true),
// so without an explicit check here an unknown key - most importantly a v2
// key like `mode:` on a section (Decision 5) - would silently decode to
// nothing instead of failing closed, half-shipping v2 vocabulary that has no
// v1 behavior at all.
var (
	prRawAllowedKeys = map[string]bool{
		"title": true, "sections": true, "labels": true, "draft": true,
		"on_agent_failure": true, "pipeline_summary": true,
	}
	prTitleAllowedKeys = map[string]bool{
		"template": true, "max_chars": true, "conventional": true,
		"strict": true, "must_match": true,
	}
	prSectionAllowedKeys = map[string]bool{
		"id": true, "heading": true, "required": true, "max_chars": true,
		"content": true, "source": true,
	}
)

func rejectUnknownYAMLKeys(value *yaml.Node, allowed map[string]bool, context string) error {
	if value.Kind != yaml.MappingNode {
		return nil // let the normal decode below produce the shape error
	}
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if !allowed[key] {
			return fmt.Errorf("%s: unknown field %q", context, key)
		}
	}
	return nil
}

func (r *PRRaw) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownYAMLKeys(value, prRawAllowedKeys, "pr"); err != nil {
		return err
	}
	type raw struct {
		Title           PRTitleRaw     `yaml:"title"`
		Sections        []PRSectionRaw `yaml:"sections"`
		Labels          []string       `yaml:"labels"`
		Draft           *bool          `yaml:"draft"`
		OnAgentFailure  string         `yaml:"on_agent_failure"`
		PipelineSummary *bool          `yaml:"pipeline_summary"`
	}
	var decoded raw
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*r = PRRaw(decoded)
	return nil
}

func (t *PRTitleRaw) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownYAMLKeys(value, prTitleAllowedKeys, "pr.title"); err != nil {
		return err
	}
	type raw struct {
		Template     string `yaml:"template"`
		MaxChars     int    `yaml:"max_chars"`
		Conventional *bool  `yaml:"conventional"`
		Strict       *bool  `yaml:"strict"`
		MustMatch    string `yaml:"must_match"`
	}
	var decoded raw
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*t = PRTitleRaw(decoded)
	return nil
}

func (s *PRSectionRaw) UnmarshalYAML(value *yaml.Node) error {
	if err := rejectUnknownYAMLKeys(value, prSectionAllowedKeys, "pr.sections[]"); err != nil {
		return err
	}
	type raw struct {
		ID       string `yaml:"id"`
		Heading  string `yaml:"heading"`
		Required *bool  `yaml:"required"`
		MaxChars int    `yaml:"max_chars"`
		Content  string `yaml:"content"`
		Source   string `yaml:"source"`
	}
	var decoded raw
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*s = PRSectionRaw(decoded)
	return nil
}

// PR is the resolved pr: config: global merged with the trusted repo copy.
type PR struct {
	// Template is nil when neither pr.title.template nor pr.sections is
	// configured at either level - the config-absent guarantee: buildPRContent
	// takes this exact nil to mean "run the legacy composer, unmodified".
	Template *prtemplate.Template
	Labels   []string
	Draft    bool
	// OnAgentFailure is always "fallback" or "fail"; validated at load.
	OnAgentFailure string
	// PipelineSummaryOnly is the resolved #601 alias: true means "drop the
	// <details> narrative but keep the signature and attestation", and is
	// only meaningful when Template is nil (an explicit template already
	// controls the floor via source: pipeline vs source: pipeline.summary).
	PipelineSummaryOnly bool
}

// buildTemplateFromRaw parses and validates a title+sections pair into a
// Template, or returns nil when nothing pr-related is configured at all -
// the config-absent guarantee. A title-only configuration (e.g. bare
// `conventional: false` or `must_match:` with no template text and no
// sections) still builds a Template whose Sections is empty: prtemplate.Build
// does not require the evidence floor in that case (see validate.go), and
// the caller (buildPRContent) keeps the legacy body composer while applying
// only the resolved title policy on top of the agent's own drafted title.
func buildTemplateFromRaw(title PRTitleRaw, sections []PRSectionRaw) (*prtemplate.Template, error) {
	hasTitlePolicy := strings.TrimSpace(title.Template) != "" ||
		title.Conventional != nil ||
		title.Strict != nil ||
		strings.TrimSpace(title.MustMatch) != "" ||
		title.MaxChars != 0
	if !hasTitlePolicy && len(sections) == 0 {
		return nil, nil
	}
	secInputs := make([]prtemplate.SectionInput, 0, len(sections))
	for _, s := range sections {
		required := false
		if s.Required != nil {
			required = *s.Required
		}
		secInputs = append(secInputs, prtemplate.SectionInput{
			ID:       s.ID,
			Heading:  s.Heading,
			Required: required,
			MaxChars: s.MaxChars,
			Content:  s.Content,
			Source:   s.Source,
		})
	}
	tmpl, err := prtemplate.Build(prtemplate.TitleInput{
		Template:     title.Template,
		MaxChars:     title.MaxChars,
		Conventional: title.Conventional,
		Strict:       title.Strict,
		MustMatch:    title.MustMatch,
	}, secInputs)
	if err != nil {
		return nil, fmt.Errorf("pr: %w", err)
	}
	return tmpl, nil
}

// validatePRRaw fails a pr: block closed at config parse time (Decision 2
// and the "Validation and bounds" list): an unknown source:, a duplicate
// id:, a missing evidence floor, an unclosed "{{", an unparseable
// must_match, an unrecognized on_agent_failure, an empty label, or an
// exceeded bound are all load-time errors, mirroring validateReviewRaw's
// rationale. This runs on the pushed copy too (see parseRepoConfig), even
// though EffectiveRepoConfig discards a pushed pr: block, so an invalid
// branch fails before merge rather than bricking the repository's pipeline
// once it becomes trusted.
//
// Title and Sections are validated as the cohesive pair they will actually
// resolve as: pr: does not merge a repo's title onto a global's sections (or
// vice versa) field-by-field - a level that configures either replaces both
// wholesale (see resolvePR) - so validating this raw copy in isolation
// checks exactly the combination that will be built at Merge time.
func validatePRRaw(pr PRRaw) error {
	if _, err := buildTemplateFromRaw(pr.Title, pr.Sections); err != nil {
		return err
	}
	for i, label := range pr.Labels {
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("pr.labels[%d] must not be empty", i)
		}
	}
	if v := strings.TrimSpace(pr.OnAgentFailure); v != "" && v != PROnAgentFailureFallback && v != PROnAgentFailureFail {
		return fmt.Errorf("pr.on_agent_failure %q is invalid; must be %q or %q", v, PROnAgentFailureFallback, PROnAgentFailureFail)
	}
	return nil
}

// resolvePR merges the global and (already trusted-scoped) repo pr: blocks
// into the resolved PR config Merge attaches to Config. Title+Sections
// replace wholesale at whichever level configures either; a level that
// configures neither inherits the other's whole template. Labels, Draft,
// OnAgentFailure, and the #601 alias merge per field, repo overriding
// global, matching every other namespace in this file.
//
// Both raw inputs have already passed validatePRRaw at load time (LoadGlobal,
// parseRepoConfig), and Merge validates nothing itself, so an error here
// indicates the two validated-in-isolation combinations somehow disagree -
// which the isolation-is-exact argument above says cannot happen. Merge has
// no error return (a config-shape change every caller would have to thread
// through for a defensive branch that should be unreachable), so a caller
// still hitting this logs and falls back to the config-absent template (nil),
// the same safe path this feature takes when unconfigured.
func resolvePR(global, repo PRRaw) (*PR, error) {
	title, sections := repo.Title, repo.Sections
	if strings.TrimSpace(title.Template) == "" && len(sections) == 0 {
		title, sections = global.Title, global.Sections
	}

	tmpl, err := buildTemplateFromRaw(title, sections)
	if err != nil {
		return nil, err
	}

	onAgentFailure := strings.TrimSpace(repo.OnAgentFailure)
	if onAgentFailure == "" {
		onAgentFailure = strings.TrimSpace(global.OnAgentFailure)
	}
	if onAgentFailure == "" {
		onAgentFailure = PROnAgentFailureFallback
	}
	if onAgentFailure != PROnAgentFailureFallback && onAgentFailure != PROnAgentFailureFail {
		return nil, fmt.Errorf("pr.on_agent_failure %q is invalid; must be %q or %q", onAgentFailure, PROnAgentFailureFallback, PROnAgentFailureFail)
	}

	labels := repo.Labels
	if labels == nil {
		labels = global.Labels
	}
	for i, label := range labels {
		if strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("pr.labels[%d] must not be empty", i)
		}
	}

	draft := false
	switch {
	case repo.Draft != nil:
		draft = *repo.Draft
	case global.Draft != nil:
		draft = *global.Draft
	}

	pipelineSummary := repo.PipelineSummary
	if pipelineSummary == nil {
		pipelineSummary = global.PipelineSummary
	}
	pipelineSummaryOnly := tmpl == nil && pipelineSummary != nil && !*pipelineSummary

	return &PR{
		Template:            tmpl,
		Labels:              labels,
		Draft:               draft,
		OnAgentFailure:      onAgentFailure,
		PipelineSummaryOnly: pipelineSummaryOnly,
	}, nil
}

// resolvePROrFallback is resolvePR with the defensive fallback documented on
// resolvePR: an error here should be unreachable given validatePRRaw already
// ran on both inputs at load time, but Merge has no error return, so a
// caller still hitting one degrades to the config-absent template rather
// than panicking or silently miscomposing a PR body.
func resolvePROrFallback(global, repo PRRaw) *PR {
	pr, err := resolvePR(global, repo)
	if err != nil {
		slog.Error("pr: config resolution failed after passing load-time validation; falling back to the unconfigured template", "error", err)
		return &PR{OnAgentFailure: PROnAgentFailureFallback}
	}
	return pr
}
