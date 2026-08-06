package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/prtemplate"
)

// jsonAgentFn returns a mockAgent.runFn that always answers with the given
// values, JSON-encoded, regardless of the prompt.
func jsonAgentFn(values map[string]any) func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	return func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		payload, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		return &agent.Result{Output: payload}, nil
	}
}

func mustBuildTemplate(t *testing.T, title prtemplate.TitleInput, sections []prtemplate.SectionInput) *prtemplate.Template {
	t.Helper()
	tmpl, err := prtemplate.Build(title, sections)
	if err != nil {
		t.Fatalf("prtemplate.Build: %v", err)
	}
	return tmpl
}

func floorOnlySections() []prtemplate.SectionInput {
	return []prtemplate.SectionInput{{ID: "pipeline", Source: "pipeline.summary"}}
}

// TestPRStep_NoTemplateUsesLegacyComposerByteForByte is the compatibility
// guarantee: sctx.Config.PR is nil in every pre-existing test (none of them
// set it), so buildPRContent's fork is unreachable and this asserts the fork
// condition itself agrees.
func TestPRStep_NoTemplateUsesLegacyComposerByteForByte(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title": "feat: add a thing",
		"body":  "## What Changed\n\n- did a thing",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA

	step := &PRStep{}
	if got := prTemplate(sctx); got != nil {
		t.Fatalf("prTemplate() = %v, want nil when pr: is unconfigured", got)
	}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if content.Title != "feat: add a thing" {
		t.Fatalf("Title = %q", content.Title)
	}
	if !strings.Contains(content.Body, "## What Changed") {
		t.Fatalf("Body = %q", content.Body)
	}
}

func TestPRStep_TemplatedBodyRespectsDeclaredOrder(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}"},
		append([]prtemplate.SectionInput{
			{ID: "whats_changed", Heading: "What's Changed", Required: true, Content: "{{ bullets }}"},
			{ID: "sister_prs", Heading: "Sister PRs", Required: false, Content: "{{ sister prs }}"},
		}, floorOnlySections()...),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1":         "APF-2531",
		"title_2":         "Unify targeting rule limits",
		"whats_changed_1": "- did a thing\n- did another thing",
		"sister_prs_1":    nil,
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}
	// The grounding check requires an identifier-shaped value to appear
	// verbatim in context (branch, commits, or intent); this run's fixture
	// commits don't mention the ticket, so put it in the intent record.
	sctx.UserIntent = "the goal is to resolve APF-2531"
	sctx.IntentSource = "agent"
	// BuildPipelineSummary renders nothing at all when the run recorded no
	// step results; insert one so the floor section has something to render.
	reviewStep, err := sctx.DB.InsertStepResult(sctx.Run.ID, "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.UpdateStepStatus(reviewStep.ID, "completed"); err != nil {
		t.Fatal(err)
	}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if content.Title != "APF-2531: Unify targeting rule limits" {
		t.Fatalf("Title = %q", content.Title)
	}
	wcIdx := strings.Index(content.Body, "## What's Changed")
	pipelineIdx := strings.Index(content.Body, "## Pipeline")
	if wcIdx < 0 || pipelineIdx < 0 || wcIdx > pipelineIdx {
		t.Fatalf("expected What's Changed before Pipeline, got body:\n%s", content.Body)
	}
	if strings.Contains(content.Body, "## Sister PRs") {
		t.Fatalf("expected the optional, unresolved Sister PRs section to drop heading included, got:\n%s", content.Body)
	}
}

func TestPRStep_UnresolvedOptionalTitleSlotElidesUnderNonStrict(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	strict := false
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}", Strict: &strict},
		floorOnlySections(),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": nil,
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if content.Title != "Unify targeting rule limits" {
		t.Fatalf("Title = %q, want no leading punctuation from the elided ticket slot", content.Title)
	}
}

// TestPRStep_GroundingAcceptsIdentifierPresentInBranchName covers mechanism
// 3: an identifier-shaped value the agent returns is published when it
// actually appears in context (here, the branch name passed to
// buildPRContent), and TestPRStep_GroundingRejectsUngroundedIdentifier is the
// negative case.
func TestPRStep_GroundingAcceptsIdentifierPresentInBranchName(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	strict := false
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}", Strict: &strict},
		floorOnlySections(),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": "APF-2531",
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature/APF-2531-unify-limits", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if content.Title != "APF-2531: Unify targeting rule limits" {
		t.Fatalf("Title = %q, want the grounded ticket ID published", content.Title)
	}
}

func TestPRStep_GroundingRejectsUngroundedIdentifier(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	strict := false
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}", Strict: &strict},
		floorOnlySections(),
	)
	// APF-9999 appears nowhere in context (branch, commits, or intent): a
	// hallucinated-looking value must be discarded, not published.
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": "APF-9999",
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature/unrelated-branch-name", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if content.Title != "Unify targeting rule limits" {
		t.Fatalf("Title = %q, want the ungrounded ticket ID discarded (elided, not published)", content.Title)
	}
}

func TestPRStep_StrictTitleFailsOnUnresolvedSlot(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}"}, // Strict defaults true
		floorOnlySections(),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": nil,
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	_, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err == nil {
		t.Fatal("expected the PR step to fail on an unresolved required title slot under strict:true")
	}
	// One re-ask before failing: the mock must have been called twice.
	if len(ag.calls) != 2 {
		t.Fatalf("agent calls = %d, want 2 (one re-ask before failing)", len(ag.calls))
	}
}

func TestPRStep_RequiredSectionFailsTheStepWhenUnresolved(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{},
		append([]prtemplate.SectionInput{
			{ID: "whats_changed", Heading: "What's Changed", Required: true, Content: "{{ bullets }}"},
		}, floorOnlySections()...),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title":           "feat: add a thing",
		"whats_changed_1": nil,
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	_, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err == nil {
		t.Fatal("expected the PR step to fail when a required section cannot be filled")
	}
}

func TestPRStep_MustMatchFailureRetriesOnceThenFailsTheStep(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	strict := false
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}", Strict: &strict, MustMatch: `^[A-Z]{2,10}-[0-9]+: .+`},
		floorOnlySections(),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": nil, // elides -> title has no ticket prefix -> fails must_match
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	_, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err == nil {
		t.Fatal("expected must_match failure to fail the PR step")
	}
	if !strings.Contains(err.Error(), "must_match") && !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v, want it to name the must_match failure", err)
	}
	if len(ag.calls) != 2 {
		t.Fatalf("agent calls = %d, want 2 (one re-ask before failing)", len(ag.calls))
	}
}

func TestPRStep_MustMatchIndependentOfStrict(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	// Every slot resolves (passes strict) but the shape is wrong for
	// must_match: the two checks catch different things.
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}", MustMatch: `^[A-Z]{2,10}-[0-9]+: .+`},
		floorOnlySections(),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": "ticket APF-2531", // resolved, but not the bare TICKET-N shape
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	_, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err == nil {
		t.Fatal("expected must_match to fail even though every slot resolved (strict would have passed)")
	}
}

// TestPRStep_ConventionalFalseWithNoTemplatePublishesDraftedTitleVerbatim
// covers the combination Decision 3 says is not reachable today at any
// setting: no title template, conventional explicitly false.
func TestPRStep_ConventionalFalseWithNoTemplatePublishesDraftedTitleVerbatim(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	conventionalOff := false
	tmpl := mustBuildTemplate(t, prtemplate.TitleInput{Conventional: &conventionalOff}, nil)
	if tmpl.HasSections() || len(tmpl.Title.Segments) > 0 {
		t.Fatalf("expected a bare-policy template with no sections and no title segments, got %+v", tmpl)
	}
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title": "Unify targeting rule limits", // not conventional-format
		"body":  "## What Changed\n\n- did a thing",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if content.Title != "Unify targeting rule limits" {
		t.Fatalf("Title = %q, want the agent's drafted title verbatim (no chore: prefix)", content.Title)
	}
}

func TestPRStep_AgentFailureFallbackKeepsFloorAndMarksTheUnappliedTemplate(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{},
		append([]prtemplate.SectionInput{
			{ID: "whats_changed", Heading: "What's Changed", Content: "{{ bullets }}"},
		}, floorOnlySections()...),
	)
	callCount := 0
	ag := &mockAgent{name: "test", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		callCount++
		return nil, errAgentBoom
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v, want fallback to succeed the step", err)
	}
	if !strings.Contains(content.Body, "template could not be applied") {
		t.Fatalf("Body = %q, want the fallback marker line", content.Body)
	}
	if callCount != 2 {
		t.Fatalf("agent calls = %d, want 2 (one re-ask before falling back)", callCount)
	}
}

func TestPRStep_AgentFailureFailModeFailsTheStep(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{},
		append([]prtemplate.SectionInput{
			{ID: "whats_changed", Heading: "What's Changed", Content: "{{ bullets }}"},
		}, floorOnlySections()...),
	)
	ag := &mockAgent{name: "test", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		return nil, errAgentBoom
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFail}

	step := &PRStep{}
	_, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err == nil {
		t.Fatal("expected on_agent_failure:fail to fail the PR step rather than publish a fallback body")
	}
}

// TestPRStep_CancelledContextIsNotRoutedThroughOnAgentFailure covers
// mechanism 7's cancellation carve-out: a cancelled run context gets neither
// the fallback body nor the fail-the-step behavior, under both settings,
// because the run is cancelling rather than the agent failing to answer.
func TestPRStep_CancelledContextIsNotRoutedThroughOnAgentFailure(t *testing.T) {
	t.Parallel()
	for _, onFailure := range []string{config.PROnAgentFailureFallback, config.PROnAgentFailureFail} {
		t.Run(onFailure, func(t *testing.T) {
			dir, baseSHA, headSHA := setupGitRepo(t)
			tmpl := mustBuildTemplate(t,
				prtemplate.TitleInput{},
				append([]prtemplate.SectionInput{
					{ID: "whats_changed", Heading: "What's Changed", Content: "{{ bullets }}"},
				}, floorOnlySections()...),
			)
			ag := &mockAgent{name: "test", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
				return nil, errAgentBoom
			}}
			sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
			sctx.Run.HeadSHA = headSHA
			sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: onFailure}

			cancelledCtx, cancel := context.WithCancel(context.Background())
			cancel()
			sctx.Ctx = cancelledCtx

			step := &PRStep{}
			content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
			if err == nil {
				t.Fatal("expected a cancelled context to surface the agent error rather than succeed with a fallback body")
			}
			if strings.Contains(content.Body, "template could not be applied") {
				t.Fatalf("Body = %q, want no fallback marker for a cancelled context", content.Body)
			}
			if strings.Contains(err.Error(), "no usable answer for the configured template") {
				t.Fatalf("error = %v, want the raw agent error, not the on_agent_failure:fail wrapping", err)
			}
		})
	}
}

func TestPRStep_MustMatchAndStrictFailuresIgnoreOnAgentFailure(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{Template: "{{ ticket }}: {{ subject }}"}, // Strict defaults true
		floorOnlySections(),
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title_1": nil,
		"title_2": "Unify targeting rule limits",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	// on_agent_failure: fallback must NOT undo the strict:true default.
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}

	step := &PRStep{}
	_, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err == nil {
		t.Fatal("expected the run to fail even though on_agent_failure is fallback")
	}
	if strings.Contains(err.Error(), "could not be applied this run") {
		t.Fatalf("error = %v, want the strict-failure error, not the on_agent_failure fallback marker", err)
	}
}

// TestDefaultTemplateEqualsLegacyComposerSectionOrder is the Path A -> Path B
// bridge claim: the design's written-down "default template" (Intent, then
// What Changed, then Risk, then Testing, then Pipeline) produces the same
// section order the legacy composer hardcodes. This does not assert
// byte-identical markdown (the templated path's What Changed section is
// agent-filled prose rather than a raw drafted body run through
// stripGeneratedSections, so exact bytes legitimately differ) - it asserts
// the one property the bridge claim is actually about: nothing in the
// section-ordering design reshuffles today's fixed order.
func TestDefaultTemplateEqualsLegacyComposerSectionOrder(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	tmpl := mustBuildTemplate(t,
		prtemplate.TitleInput{},
		[]prtemplate.SectionInput{
			{ID: "intent", Source: "run.intent"},
			{ID: "what_changed", Heading: "What Changed", Required: true, Content: "{{ what changed }}"},
			{ID: "risk", Source: "pipeline.risk"},
			{ID: "testing", Source: "pipeline.testing"},
			{ID: "pipeline", Source: "pipeline"},
		},
	)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title":          "feat: add a thing",
		"what_changed_1": "- did a thing",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{Template: tmpl, OnAgentFailure: config.PROnAgentFailureFallback}
	sctx.UserIntent = "user wanted to add a thing"

	testStep, err := sctx.DB.InsertStepResult(sctx.Run.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.UpdateStepStatus(testStep.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.SetStepFindings(testStep.ID, `{"findings":[],"testing_summary":"ran the unit tests"}`); err != nil {
		t.Fatal(err)
	}
	reviewStep, err := sctx.DB.InsertStepResult(sctx.Run.ID, "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.UpdateStepStatus(reviewStep.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.SetStepFindings(reviewStep.ID, `{"findings":[],"risk_level":"low"}`); err != nil {
		t.Fatal(err)
	}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}

	order := []string{"## Intent", "## What Changed", "## Risk Assessment", "## Testing", "## Pipeline"}
	lastIdx := -1
	for _, heading := range order {
		idx := strings.Index(content.Body, heading)
		if idx < 0 {
			continue // an absent optional section is legal; order among present ones still must hold
		}
		if idx <= lastIdx {
			t.Fatalf("heading %q out of order (want: %v), got body:\n%s", heading, order, content.Body)
		}
		lastIdx = idx
	}
	if !strings.Contains(content.Body, "## Intent") || !strings.Contains(content.Body, "## Pipeline") {
		t.Fatalf("expected both Intent and Pipeline present, got:\n%s", content.Body)
	}
}

func TestPRStep_PipelineSummaryAliasDropsDetailsButKeepsAttestation(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := &mockAgent{name: "test", runFn: jsonAgentFn(map[string]any{
		"title": "feat: add a thing",
		"body":  "## What Changed\n\n- did a thing",
	})}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.HeadSHA = headSHA
	sctx.Config.PR = &config.PR{PipelineSummaryOnly: true, OnAgentFailure: config.PROnAgentFailureFallback}

	reviewStep, err := sctx.DB.InsertStepResult(sctx.Run.ID, "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.UpdateStepStatus(reviewStep.ID, "completed"); err != nil {
		t.Fatal(err)
	}

	step := &PRStep{}
	content, err := step.buildPRContent(sctx, "feature", baseSHA, 0)
	if err != nil {
		t.Fatalf("buildPRContent: %v", err)
	}
	if !strings.Contains(content.Body, noMistakesPRSignature) {
		t.Fatalf("Body missing the no-mistakes signature: %q", content.Body)
	}
	if strings.Contains(content.Body, "<details>") {
		t.Fatalf("Body = %q, want <details> dropped under the pipeline_summary alias", content.Body)
	}
}

func TestDescriptionIntentPromptSection_NotAuthoritativeFraming(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	sctx.UserIntent = "add rate limiting to the ingest API"
	sctx.IntentSource = "claude" // inferred, not the authoritative "agent"/"rerun" source

	section := descriptionIntentPromptSection(sctx)
	if section == "" {
		t.Fatal("expected a non-empty section when UserIntent is set")
	}
	if strings.Contains(section, "AUTHORITATIVE") {
		t.Fatalf("descriptionIntentPromptSection = %q, must not use the review step's authoritative framing", section)
	}
	if !strings.Contains(section, "BEGIN USER INTENT") || !strings.Contains(section, "add rate limiting") {
		t.Fatalf("descriptionIntentPromptSection = %q, expected the intent text to be present", section)
	}
}

var errAgentBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "agent boom" }
