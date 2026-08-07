package steps

import (
	"context"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

// capabilityHost is a minimal scm.Host whose only job is reporting a fixed
// Capabilities() and Provider(), so applyPRMetadata's gating can be tested
// without a real provider CLI.
type capabilityHost struct {
	provider scm.Provider
	caps     scm.Capabilities
}

func (h capabilityHost) Provider() scm.Provider         { return h.provider }
func (h capabilityHost) Capabilities() scm.Capabilities { return h.caps }
func (capabilityHost) Available(context.Context) error  { return nil }
func (capabilityHost) FindPR(context.Context, string, string) (*scm.PR, error) {
	return nil, nil
}
func (capabilityHost) CreatePR(context.Context, string, string, scm.PRContent) (*scm.PR, error) {
	return nil, nil
}
func (capabilityHost) UpdatePR(context.Context, *scm.PR, scm.PRContent) (*scm.PR, error) {
	return nil, nil
}
func (capabilityHost) GetPRState(context.Context, *scm.PR) (scm.PRState, error) {
	return "", nil
}
func (capabilityHost) GetChecks(context.Context, *scm.PR) ([]scm.Check, error) {
	return nil, nil
}
func (capabilityHost) GetMergeableState(context.Context, *scm.PR) (scm.MergeableState, error) {
	return "", scm.ErrUnsupported
}
func (capabilityHost) FetchFailedCheckLogs(context.Context, *scm.PR, string, string, []string) (string, error) {
	return "", scm.ErrUnsupported
}

var _ scm.Host = capabilityHost{}

func TestApplyPRMetadata_NoOpWhenConfigAbsent(t *testing.T) {
	sctx := &pipeline.StepContext{Config: &config.Config{}, Log: func(string) {}}
	host := capabilityHost{provider: scm.ProviderGitHub, caps: scm.Capabilities{PRLabels: true, PRDraft: true}}
	content := &scm.PRContent{Title: "t", Body: "b"}

	applyPRMetadata(sctx, host, content)

	if len(content.Labels) != 0 || content.Draft {
		t.Fatalf("content = %+v, want untouched when pr: is unconfigured", content)
	}
}

func TestApplyPRMetadata_AppliesLabelsAndDraftWhenSupported(t *testing.T) {
	sctx := &pipeline.StepContext{
		Config: &config.Config{PR: &config.PR{Labels: []string{"automated"}, Draft: true}},
		Log:    func(string) {},
	}
	host := capabilityHost{provider: scm.ProviderGitHub, caps: scm.Capabilities{PRLabels: true, PRDraft: true}}
	content := &scm.PRContent{Title: "t", Body: "b"}

	applyPRMetadata(sctx, host, content)

	if len(content.Labels) != 1 || content.Labels[0] != "automated" {
		t.Fatalf("Labels = %v, want [automated]", content.Labels)
	}
	if !content.Draft {
		t.Fatal("Draft = false, want true")
	}
}

func TestApplyPRMetadata_SkipsWithNoteWhenProviderLacksCapability(t *testing.T) {
	var logged []string
	sctx := &pipeline.StepContext{
		Config: &config.Config{PR: &config.PR{Labels: []string{"automated"}, Draft: true}},
		Log:    func(s string) { logged = append(logged, s) },
	}
	host := capabilityHost{provider: scm.ProviderGitLab, caps: scm.Capabilities{}}
	content := &scm.PRContent{Title: "t", Body: "b"}

	applyPRMetadata(sctx, host, content)

	if len(content.Labels) != 0 {
		t.Fatalf("Labels = %v, want none when the provider lacks PRLabels", content.Labels)
	}
	if content.Draft {
		t.Fatal("Draft = true, want false when the provider lacks PRDraft")
	}
	if len(logged) != 2 {
		t.Fatalf("logged = %v, want a step note for each unsupported affordance", logged)
	}
}

func TestApplyPRMetadata_NoLabelsOrDraftConfiguredIsANoOp(t *testing.T) {
	var logged []string
	sctx := &pipeline.StepContext{
		Config: &config.Config{PR: &config.PR{}},
		Log:    func(s string) { logged = append(logged, s) },
	}
	host := capabilityHost{provider: scm.ProviderGitHub, caps: scm.Capabilities{}}
	content := &scm.PRContent{Title: "t", Body: "b"}

	applyPRMetadata(sctx, host, content)

	if len(logged) != 0 {
		t.Fatalf("logged = %v, want no step notes when pr.labels/pr.draft are both unset", logged)
	}
}
