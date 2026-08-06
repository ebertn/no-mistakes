package config

import "testing"

func TestLoadRepo_PRRawParsedFromYAML(t *testing.T) {
	yamlText := `
pr:
  title:
    template: "{{ ticket }}: {{ subject }}"
    strict: false
  sections:
    - id: whats_changed
      heading: "What's Changed"
      required: true
      content: "{{ bullets }}"
    - source: pipeline.summary
`
	cfg, err := LoadRepoFromBytes([]byte(yamlText))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.PR.Title.Template != "{{ ticket }}: {{ subject }}" {
		t.Fatalf("Title.Template = %q", cfg.PR.Title.Template)
	}
	if len(cfg.PR.Sections) != 2 {
		t.Fatalf("Sections = %+v", cfg.PR.Sections)
	}
}

func TestPRMerge_RepoOverridesGlobal(t *testing.T) {
	global := &GlobalConfig{PR: PRRaw{
		Title:  PRTitleRaw{Template: "{{ global }}"},
		Labels: []string{"global-label"},
	}}
	repo := &RepoConfig{PR: PRRaw{
		Title:  PRTitleRaw{Template: "{{ ticket }}: {{ subject }}"},
		Labels: []string{"repo-label"},
		Sections: []PRSectionRaw{
			{Source: "pipeline.summary"},
		},
	}}

	cfg := Merge(global, repo)
	if cfg.PR == nil || cfg.PR.Template == nil {
		t.Fatal("expected repo's template to resolve")
	}
	if len(cfg.PR.Labels) != 1 || cfg.PR.Labels[0] != "repo-label" {
		t.Fatalf("Labels = %v, want repo's labels to win", cfg.PR.Labels)
	}
}

func TestPRMerge_RepoInheritsGlobalTemplateWhenRepoConfiguresNeither(t *testing.T) {
	global := &GlobalConfig{PR: PRRaw{
		Title: PRTitleRaw{Template: "{{ ticket }}: {{ subject }}"},
		Sections: []PRSectionRaw{
			{ID: "wc", Content: "{{ bullets }}"},
			{Source: "pipeline.summary"},
		},
	}}
	repo := &RepoConfig{}

	cfg := Merge(global, repo)
	if cfg.PR == nil || cfg.PR.Template == nil {
		t.Fatal("expected the global template to apply when the repo configures nothing")
	}
}

func TestPRMerge_AbsentAtBothLevelsProducesNilTemplate(t *testing.T) {
	cfg := Merge(&GlobalConfig{}, &RepoConfig{})
	if cfg.PR == nil {
		t.Fatal("expected a resolved PR config even when unconfigured")
	}
	if cfg.PR.Template != nil {
		t.Fatal("expected a nil Template when pr: is absent at both levels (the config-absent guarantee)")
	}
	if cfg.PR.OnAgentFailure != PROnAgentFailureFallback {
		t.Fatalf("OnAgentFailure = %q, want the fallback default", cfg.PR.OnAgentFailure)
	}
}

func TestPRMerge_OnAgentFailureAndDraftAndPipelineSummaryMergePerField(t *testing.T) {
	global := &GlobalConfig{PR: PRRaw{OnAgentFailure: "fail", PipelineSummary: boolPtr(false)}}
	repo := &RepoConfig{}
	cfg := Merge(global, repo)
	if cfg.PR.OnAgentFailure != "fail" {
		t.Fatalf("OnAgentFailure = %q, want global's value to apply when repo sets none", cfg.PR.OnAgentFailure)
	}
	if !cfg.PR.PipelineSummaryOnly {
		t.Fatal("expected the #601 alias to resolve pipeline_summary:false to PipelineSummaryOnly")
	}

	repoOverride := &RepoConfig{PR: PRRaw{OnAgentFailure: "fallback", Draft: boolPtr(true)}}
	cfg2 := Merge(global, repoOverride)
	if cfg2.PR.OnAgentFailure != "fallback" {
		t.Fatalf("OnAgentFailure = %q, want repo's override to win", cfg2.PR.OnAgentFailure)
	}
	if !cfg2.PR.Draft {
		t.Fatal("expected repo's draft:true to win")
	}
}

func TestEffectiveRepoConfig_PRTrustedOnly(t *testing.T) {
	pushed := &RepoConfig{PR: PRRaw{Title: PRTitleRaw{Template: "{{ attacker-controlled }}"}}}
	trusted := &RepoConfig{PR: PRRaw{
		Title:    PRTitleRaw{Template: "{{ ticket }}: {{ subject }}"},
		Sections: []PRSectionRaw{{Source: "pipeline.summary"}},
	}}

	got := EffectiveRepoConfig(pushed, trusted, false)
	if got.PR.Title.Template != "{{ ticket }}: {{ subject }}" {
		t.Fatalf("PR.Title.Template = %q, want the trusted copy's template", got.PR.Title.Template)
	}

	got = EffectiveRepoConfig(pushed, &RepoConfig{}, false)
	if got.PR.Title.Template != "" {
		t.Fatalf("PR.Title.Template = %q, want empty (pushed-only value must be ignored)", got.PR.Title.Template)
	}

	got = EffectiveRepoConfig(pushed, nil, false)
	if got.PR.Title.Template != "" {
		t.Fatalf("PR.Title.Template = %q, want empty without a trusted copy", got.PR.Title.Template)
	}

	// allow_repo_commands is scoped to commands/agent; pr: stays trusted-only
	// under the opt-in too (Decision 6).
	got = EffectiveRepoConfig(pushed, &RepoConfig{}, true)
	if got.PR.Title.Template != "" {
		t.Fatalf("PR.Title.Template = %q, want empty (allow_repo_commands must not let a pushed pr: block through)", got.PR.Title.Template)
	}
}

func TestEffectiveRepoConfig_PRLabelsMustMatchAndOnAgentFailureTrustedOnly(t *testing.T) {
	pushed := &RepoConfig{PR: PRRaw{
		Labels:         []string{"self-approved"},
		OnAgentFailure: "fallback",
		Title:          PRTitleRaw{MustMatch: ".*"},
	}}
	trusted := &RepoConfig{PR: PRRaw{
		Labels:         []string{"needs-review"},
		OnAgentFailure: "fail",
	}}

	got := EffectiveRepoConfig(pushed, trusted, false)
	if len(got.PR.Labels) != 1 || got.PR.Labels[0] != "needs-review" {
		t.Fatalf("Labels = %v, want the trusted copy's labels; a pushed branch must not write its own forge labels", got.PR.Labels)
	}
	if got.PR.OnAgentFailure != "fail" {
		t.Fatalf("OnAgentFailure = %q, want the trusted copy's stricter posture; a pushed branch must not relax it", got.PR.OnAgentFailure)
	}
}

func TestValidatePRRaw_RejectsUnparseableMustMatch(t *testing.T) {
	_, err := LoadRepoFromBytes([]byte("pr:\n  title:\n    must_match: \"(\"\n"))
	if err == nil {
		t.Fatal("expected a config-load error for an unparseable must_match regex")
	}
}

func TestValidatePRRaw_RejectsEmptyLabel(t *testing.T) {
	_, err := LoadRepoFromBytes([]byte("pr:\n  labels: [\"ok\", \"\"]\n"))
	if err == nil {
		t.Fatal("expected a config-load error for an empty label")
	}
}

func TestValidatePRRaw_RejectsUnknownOnAgentFailureValue(t *testing.T) {
	_, err := LoadRepoFromBytes([]byte("pr:\n  on_agent_failure: retry\n"))
	if err == nil {
		t.Fatal("expected a config-load error for an unrecognized on_agent_failure value")
	}
}

func TestValidatePRRaw_RejectsMissingFloor(t *testing.T) {
	_, err := LoadRepoFromBytes([]byte("pr:\n  sections:\n    - id: x\n      content: \"{{ y }}\"\n"))
	if err == nil {
		t.Fatal("expected a config-load error for sections missing the evidence floor")
	}
}

func TestValidatePRRaw_RunsOnPushedCopyToo(t *testing.T) {
	// parseRepoConfig validates every .no-mistakes.yaml it reads, regardless
	// of whether the caller will end up treating it as trusted or pushed -
	// this is what makes an invalid branch fail before it can ever become
	// the trusted copy.
	_, err := LoadRepoFromBytes([]byte("pr:\n  title:\n    must_match: \"(\"\n"))
	if err == nil {
		t.Fatal("expected LoadRepoFromBytes (used for both pushed and trusted reads) to validate pr:")
	}
}

func TestPRSection_ModeKeyIsAnUnknownFieldErrorInV1(t *testing.T) {
	_, err := LoadRepoFromBytes([]byte("pr:\n  sections:\n    - source: run.intent\n      mode: rewrite\n"))
	if err == nil {
		t.Fatal("expected mode: on a section to be an unknown-field error (Decision 5: no half-shipped v2 vocabulary)")
	}
}

func TestPRRaw_UnknownTopLevelKeyIsRejected(t *testing.T) {
	_, err := LoadRepoFromBytes([]byte("pr:\n  template_file: PULL_REQUEST_TEMPLATE.md\n"))
	if err == nil {
		t.Fatal("expected an unknown pr: key to be rejected")
	}
}

func boolPtr(b bool) *bool { return &b }
