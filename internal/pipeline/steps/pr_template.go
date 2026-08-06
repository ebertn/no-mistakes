package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/conventional"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/prtemplate"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	maxPRTemplateCommitLogCommits = 40
	maxPRTemplateCommitLogBytes   = 8 * 1024
)

// buildTemplatedPRContent replaces buildPRContent's legacy composition when a
// pr: template configures pr.sections or a title.template with its own
// {{ }} placeholders. It extends the same PR-drafting agent invocation
// buildPRContent already makes - one prompt, one structured-output call -
// rather than adding a pipeline step: the agent returns one value per
// placeholder (plus a free-form title and/or body when the template leaves
// one of those untemplated), and no-mistakes owns every other rendering
// decision (order, headings, elision, truncation, the evidence floor).
func (s *PRStep) buildTemplatedPRContent(sctx *pipeline.StepContext, tmpl *prtemplate.Template, branch, baseSHA string, bodyLimit int) (prContent, error) {
	ctx := sctx.Ctx
	diffStat, _ := git.Run(ctx, sctx.WorkDir, "diff", "--stat", baseSHA+".."+sctx.Run.HeadSHA)
	finalDiff, err := git.Run(ctx, sctx.WorkDir, "diff", "--name-status", baseSHA+".."+sctx.Run.HeadSHA)
	if err != nil {
		return prContent{}, fmt.Errorf("read final branch diff: %w", err)
	}
	pipelineMD, riskLine, testingMD := s.buildPipelineSection(sctx)
	commitLog := commitMessagesContext(ctx, sctx.WorkDir, baseSHA, sctx.Run.HeadSHA)
	intentText := cleanedUserIntent(sctx)

	needsFreeTitle := len(tmpl.Title.Segments) == 0
	needsFreeBody := !tmpl.HasSections()

	schema := buildTemplatedSchema(tmpl, needsFreeTitle, needsFreeBody)
	prompt := buildTemplatedPrompt(sctx, tmpl, branch, baseSHA, diffStat, finalDiff, commitLog, needsFreeTitle, needsFreeBody, bodyLimit)

	draft, draftErr := draftTemplatedContentWithRetry(sctx, prompt, schema)
	if draftErr != nil {
		return templatedAgentFailureContent(sctx, finalDiff, riskLine, testingMD, pipelineMD, bodyLimit, draftErr)
	}

	groundingContext := []string{branch, commitLog, intentText}
	resolution := resolveTemplatedDraft(sctx, tmpl, draft, groundingContext, pipelineMD, riskLine, testingMD, bodyLimit)
	if len(resolution.problems) > 0 {
		slog.Warn("pr: templated content incomplete, re-asking once", "problems", resolution.problems)
		retryPrompt := prompt + "\n\nYour previous answer had problems that must be fixed:\n- " +
			strings.Join(resolution.problems, "\n- ") + "\nReturn corrected JSON matching the schema."
		draft2, err2 := draftTemplatedContent(sctx, retryPrompt, schema)
		if err2 != nil {
			return templatedAgentFailureContent(sctx, finalDiff, riskLine, testingMD, pipelineMD, bodyLimit, err2)
		}
		resolution = resolveTemplatedDraft(sctx, tmpl, draft2, groundingContext, pipelineMD, riskLine, testingMD, bodyLimit)
		if len(resolution.problems) > 0 {
			return prContent{}, fmt.Errorf("pr: template could not be resolved after a retry: %s", strings.Join(resolution.problems, "; "))
		}
	}

	return prContent{Title: resolution.title, Body: resolution.body}, nil
}

// commitMessagesContext returns commit subjects and bodies for base..head,
// oldest first, bounded so the prompt stays predictable in size. This is the
// one genuinely new context item the templated prompt needs: it is where a
// human already writes the facts a template asks for that are not in the
// diff (a ticket ID, a sister PR, trailers), and today's prompt does not
// include it at all.
func commitMessagesContext(ctx context.Context, workDir, baseSHA, headSHA string) string {
	out, err := git.Run(ctx, workDir, "log", "--reverse",
		fmt.Sprintf("--max-count=%d", maxPRTemplateCommitLogCommits),
		"--format=%s%n%b%n----", baseSHA+".."+headSHA)
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if len(out) > maxPRTemplateCommitLogBytes {
		out = out[:maxPRTemplateCommitLogBytes]
	}
	return out
}

// templatedDraft is the agent's raw structured answer for one drafting
// invocation: per-slot values, plus a free-form title and/or body when the
// template leaves one of those untemplated.
type templatedDraft struct {
	values map[string]*string
	title  string
	body   string
}

// draftTemplatedContent runs one drafting agent invocation and parses its
// structured output. It never applies template semantics (elision,
// required-ness, grounding) - resolveTemplatedDraft owns that.
func draftTemplatedContent(sctx *pipeline.StepContext, prompt string, schema json.RawMessage) (*templatedDraft, error) {
	result, err := sctx.Agent.Run(sctx.Ctx, agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: schema,
		OnChunk:    sctx.LogChunk,
	})
	if err != nil {
		return nil, err
	}
	if len(result.Output) == 0 {
		return nil, errors.New("agent returned no structured output")
	}
	var raw map[string]*string
	if err := json.Unmarshal(result.Output, &raw); err != nil {
		return nil, fmt.Errorf("parse structured output: %w", err)
	}
	draft := &templatedDraft{values: raw}
	if t := raw["title"]; t != nil {
		draft.title = strings.TrimSpace(*t)
	}
	if b := raw["body"]; b != nil {
		draft.body = strings.TrimSpace(*b)
	}
	return draft, nil
}

// draftTemplatedContentWithRetry is the total-failure class (Decision 8's
// on_agent_failure question): the invocation itself crashed, timed out, or
// produced output that does not parse against the schema even after this one
// re-ask. A cancelled run context gets no retry and no dressing-up.
func draftTemplatedContentWithRetry(sctx *pipeline.StepContext, prompt string, schema json.RawMessage) (*templatedDraft, error) {
	draft, err := draftTemplatedContent(sctx, prompt, schema)
	if err == nil {
		return draft, nil
	}
	if sctx.Ctx.Err() != nil {
		return nil, err
	}
	slog.Warn("pr: templated drafting failed, re-asking once", "error", err)
	retryPrompt := prompt + fmt.Sprintf("\n\nYour previous response could not be used (%s). Return JSON that matches the schema exactly - no markdown fences, no extra keys.", err)
	return draftTemplatedContent(sctx, retryPrompt, schema)
}

// templatedAgentFailureContent implements Decision 8: fallback (default)
// keeps today's deterministic fallback body plus a marker noting the
// template could not be applied; fail parks the run. A cancelled context
// takes neither branch - the run is cancelling, not failing.
func templatedAgentFailureContent(sctx *pipeline.StepContext, finalDiff, riskLine, testingMD, pipelineMD string, bodyLimit int, cause error) (prContent, error) {
	if sctx.Ctx.Err() != nil {
		return prContent{}, cause
	}
	onFailure := config.PROnAgentFailureFallback
	if sctx.Config != nil && sctx.Config.PR != nil && sctx.Config.PR.OnAgentFailure != "" {
		onFailure = sctx.Config.PR.OnAgentFailure
	}
	if onFailure == config.PROnAgentFailureFail {
		return prContent{}, fmt.Errorf("pr: drafting agent produced no usable answer for the configured template: %w", cause)
	}
	slog.Warn("pr: drafting agent failed for the configured template, using the deterministic fallback", "error", cause)
	content := fallbackPRContent(sctx, finalDiff, riskLine, testingMD, pipelineMD, bodyLimit)
	content.Body = appendTemplateFallbackMarker(content.Body)
	return content, nil
}

func appendTemplateFallbackMarker(body string) string {
	const marker = "_... (the configured pr: template could not be applied this run; this is the deterministic fallback body.)_"
	if strings.TrimSpace(body) == "" {
		return marker
	}
	return body + "\n\n" + marker
}

// templatedResolution is the fully rendered result of one drafting attempt,
// or the list of problems (unresolved required values, a must_match
// mismatch) that must be fixed before it can be published.
type templatedResolution struct {
	title    string
	body     string
	problems []string
}

// resolveTemplatedDraft applies every rendering and determinism mechanism to
// one drafted answer: the grounding check, elision, required/strict
// enforcement, conventional tightening, max_chars truncation, must_match,
// and (when sections are configured) section-by-section rendering plus
// provider-limit shedding via prtemplate.AssembleBody.
func resolveTemplatedDraft(sctx *pipeline.StepContext, tmpl *prtemplate.Template, draft *templatedDraft, groundingContext []string, pipelineMD, riskLine, testingMD string, bodyLimit int) templatedResolution {
	values := groundValues(draft.values, groundingContext)

	var problems []string

	title := draft.title
	if len(tmpl.Title.Segments) > 0 {
		title = prtemplate.RenderSegments(tmpl.Title.Segments, values)
		if tmpl.Title.Strict {
			if unresolved := prtemplate.UnresolvedSlotKeys(tmpl.Title.Segments, values); len(unresolved) > 0 {
				problems = append(problems, fmt.Sprintf("title placeholder(s) %s could not be determined", strings.Join(unresolved, ", ")))
			}
		}
	} else if strings.TrimSpace(title) == "" {
		problems = append(problems, "the drafted title was empty")
	}
	if tmpl.Title.Conventional {
		title = conventional.TightenTitle(title)
	}
	if tmpl.Title.MaxChars > 0 {
		title = prtemplate.TruncateAtLineBoundary(title, tmpl.Title.MaxChars, "")
	}
	if tmpl.Title.MustMatch != nil && strings.TrimSpace(title) != "" && !tmpl.Title.MustMatch.Pattern.MatchString(title) {
		problems = append(problems, fmt.Sprintf("title %q does not match pr.title.must_match %q", title, tmpl.Title.MustMatch.Raw))
	}

	var body string
	if tmpl.HasSections() {
		rendered := make([]prtemplate.RenderedSection, 0, len(tmpl.Sections))
		for _, section := range tmpl.Sections {
			block, sectionProblem := renderTemplatedSection(sctx, section, values, pipelineMD, riskLine, testingMD)
			if sectionProblem != "" {
				problems = append(problems, sectionProblem)
			}
			rendered = append(rendered, prtemplate.RenderedSection{Section: section, Markdown: block})
		}
		body = prtemplate.AssembleBody(rendered, bodyLimit)
	} else {
		body = draft.body
		if strings.TrimSpace(body) == "" {
			problems = append(problems, "the drafted body was empty")
		} else {
			body = unwrapNestedPRBody(body)
			body = stripGeneratedSections(body)
			if bodyLimit > 0 {
				body = assemblePRBody(sctx, body, riskLine, testingMD, pipelineMD, bodyLimit)
			} else {
				body = buildPRBody(body, riskLine, testingMD, pipelineMD, sctx)
			}
		}
	}

	return templatedResolution{title: title, body: body, problems: problems}
}

// renderTemplatedSection renders one section's markdown block (heading
// included), given already-grounded slot values and the deterministic
// pipeline/risk/testing blocks the PR step already computed. Returns "" for
// an optional section with nothing to say (dropping it, heading included)
// and a non-empty problem string only when a REQUIRED section has nothing to
// say.
func renderTemplatedSection(sctx *pipeline.StepContext, section prtemplate.Section, values map[string]*string, pipelineMD, riskLine, testingMD string) (block, problem string) {
	switch section.Source {
	case prtemplate.SourceRunIntent:
		if cleaned := cleanedUserIntent(sctx); cleaned != "" {
			block = "## " + sectionHeadingOrDefault(section, "Intent") + "\n\n" + cleaned
		}
	case prtemplate.SourcePipelineRisk:
		if riskLine != "" {
			block = "## " + sectionHeadingOrDefault(section, "Risk Assessment") + "\n\n" + riskLine
		}
	case prtemplate.SourcePipelineTesting:
		block = overrideSectionHeading(testingMD, "Testing", section.Heading)
	case prtemplate.SourcePipelineSummary:
		header, _ := splitPipelineSectionHeader(pipelineMD)
		block = overrideSectionHeading(strings.TrimRight(header, "\n"), "Pipeline", section.Heading)
	case prtemplate.SourcePipeline:
		block = overrideSectionHeading(pipelineMD, "Pipeline", section.Heading)
	default:
		block = prtemplate.RenderContentSection(section, values)
		if block == "" && section.Required {
			problem = fmt.Sprintf("required section %q could not be filled", sectionLabelOrDefault(section))
		}
	}
	return block, problem
}

func sectionHeadingOrDefault(section prtemplate.Section, def string) string {
	if strings.TrimSpace(section.Heading) != "" {
		return section.Heading
	}
	return def
}

func sectionLabelOrDefault(section prtemplate.Section) string {
	if strings.TrimSpace(section.Heading) != "" {
		return section.Heading
	}
	if strings.TrimSpace(section.ID) != "" {
		return section.ID
	}
	return "unnamed section"
}

// overrideSectionHeading swaps a deterministic block's own leading "## def"
// heading for a custom one, when the section declared heading: - the
// deterministic CONTENTS (signature, attestation, risk line, testing
// summary) never change, only the label above them (source: vocabulary
// table: "Heading (overridable)").
func overrideSectionHeading(md, def, custom string) string {
	custom = strings.TrimSpace(custom)
	if custom == "" || custom == def || md == "" {
		return md
	}
	prefix := "## " + def
	if !strings.HasPrefix(md, prefix) {
		return md
	}
	return "## " + custom + strings.TrimPrefix(md, prefix)
}

// groundValues applies the identifier-groundedness check (mechanism 3) to
// every value the agent returned: an identifier-shaped value that does not
// appear verbatim in context is treated as unresolved rather than published.
// Prose is returned untouched.
func groundValues(raw map[string]*string, groundingContext []string) map[string]*string {
	out := make(map[string]*string, len(raw))
	for key, value := range raw {
		if value == nil {
			out[key] = nil
			continue
		}
		v := *value
		if prtemplate.IsIdentifierShaped(v) && !prtemplate.IsGrounded(v, groundingContext...) {
			slog.Warn("pr: ungrounded identifier-shaped value discarded", "slot", key, "value", v)
			out[key] = nil
			continue
		}
		out[key] = value
	}
	return out
}

// buildTemplatedSchema derives the structured-output schema for one drafting
// invocation: prtemplate.BuildSchema's one-nullable-property-per-slot schema,
// plus a free-form "title" and/or "body" property when the template leaves
// that half untemplated (a title-only template still needs a body from
// somewhere; a sections-only template still needs a title from somewhere).
func buildTemplatedSchema(tmpl *prtemplate.Template, needsFreeTitle, needsFreeBody bool) json.RawMessage {
	raw := prtemplate.BuildSchema(tmpl)
	if !needsFreeTitle && !needsFreeBody {
		return raw
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	required, _ := doc["required"].([]any)

	if needsFreeTitle {
		titleMax := tmpl.Title.MaxChars
		if titleMax <= 0 {
			titleMax = prtemplate.MaxPRTitleChars
		}
		props["title"] = map[string]any{
			"type":        "string",
			"maxLength":   titleMax,
			"description": "The PR title, in the exact final form to publish (conventional-commit format if this repo uses it).",
		}
		required = append(required, "title")
	}
	if needsFreeBody {
		props["body"] = map[string]any{
			"type":        "string",
			"description": `A "## What Changed" section in GitHub-flavored markdown: 1-3 concise bullet points describing the concrete changes in this branch. Do not include Intent, Risk Assessment, Testing, or Pipeline sections.`,
		}
		required = append(required, "body")
	}

	doc["properties"] = props
	doc["required"] = required
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// buildTemplatedPrompt assembles the one drafting prompt: the same context
// the legacy composer already gathers, plus the commit-message context and
// step-results digest the design adds, plus one labeled brief per
// placeholder in template order, plus the output contract (return JSON only,
// null is the correct answer for a fact this change does not have, never a
// stand-in like "N/A", and an identifier must be copied verbatim from the
// context or left null).
func buildTemplatedPrompt(sctx *pipeline.StepContext, tmpl *prtemplate.Template, branch, baseSHA, diffStat, finalDiff, commitLog string, needsFreeTitle, needsFreeBody bool, bodyLimit int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Draft a pull request title and description for the full branch delta, one value per placeholder listed below.\n\n")
	fmt.Fprintf(&b, "Context:\n- branch: %s\n- base commit: %s\n- target commit: %s\n- default branch: %s\n\n", branch, baseSHA, sctx.Run.HeadSHA, sctx.Repo.DefaultBranch)

	fmt.Fprintf(&b, "Diff stat:\n%s\n\n", diffStat)
	fmt.Fprintf(&b, "Final diff paths and statuses:\n%s\n\n", finalDiff)
	fmt.Fprintf(&b, "Derive every claim from the final diff; inspect it directly when the paths and statuses above do not provide enough detail. Do not invent tests or behavior.\n\n")

	if commitLog != "" {
		fmt.Fprintf(&b, "Commit subjects and bodies for this branch, oldest first (this is often where a ticket ID, sister PR, or ADR reference is written down, in the subject, body, or a trailer):\n%s\n\n", commitLog)
	}

	b.WriteString(descriptionIntentPromptSection(sctx))
	b.WriteString(stepResultsDigest(sctx))

	if needsFreeTitle && (tmpl.Title.Segments == nil) && titleShouldFollowConventionalRule(tmpl) {
		fmt.Fprintf(&b, "\n\nTitle must use conventional commit format: \"type(scope): description\" or \"type: description\". Valid types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert.\n%s\n", conventional.ReleaseTypeRule)
	}

	b.WriteString("\n\nPlaceholders to fill, one JSON property per placeholder:\n")
	for _, slot := range tmpl.Slots() {
		fmt.Fprintf(&b, "- %s: %s\n", slot.Key, slot.Brief)
	}
	if needsFreeTitle {
		b.WriteString("- title: the PR title (see the output contract; this one may not be null).\n")
	}
	if needsFreeBody {
		b.WriteString("- body: the \"## What Changed\" section (see the output contract; this one may not be null).\n")
	}

	b.WriteString("\n\nOutput contract:\n")
	b.WriteString("- Return JSON only. No markdown fences, no headings, no \"##\".\n")
	b.WriteString("- For any placeholder you cannot determine from the context above, return null. Do not guess, do not invent, and do not write a stand-in.\n")
	b.WriteString("- Never return \"N/A\", \"None\", \"TBD\", \"See above\", \"unknown\", or an empty string for a placeholder. null is the correct answer for a fact this change does not have.\n")
	b.WriteString("- For an identifier (a ticket key, PR number, commit SHA, service name), use the exact text as it appears in the branch name, commit messages, or intent above. If it appears nowhere there, return null.\n")

	b.WriteString(prBodyBudgetPromptSection(bodyLimit))

	return b.String()
}

// titleShouldFollowConventionalRule reports whether the free-form title the
// agent drafts should aim for conventional-commit shape up front: true
// whenever the resolved policy will tighten it anyway (Conventional), since
// asking the agent to get it right the first time is strictly better than
// relying on the post-hoc rewrite.
func titleShouldFollowConventionalRule(tmpl *prtemplate.Template) bool {
	return tmpl.Title.Conventional
}

// stepResultsDigest is a compact per-step status/finding-count summary, the
// design's context item 6. It is a much smaller re-read of what
// buildPipelineSection already queried in this same step invocation; kept
// separate rather than threading the raw step/round slices through, so this
// file does not need to change buildPipelineSection's signature.
func stepResultsDigest(sctx *pipeline.StepContext) string {
	steps, err := sctx.DB.GetStepsByRun(sctx.Run.ID)
	if err != nil || len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThis run's step results so far (status and finding counts; use only to inform \"what the pipeline had to fix\"-style placeholders, never to reproduce as prose verbatim):\n")
	for _, sr := range steps {
		if sr == nil || sr.StepName == "" {
			continue
		}
		count := 0
		if sr.FindingsJSON != nil {
			if f, err := types.ParseFindingsJSON(*sr.FindingsJSON); err == nil {
				count = len(f.Items)
			}
		}
		fmt.Fprintf(&b, "- %s: %s (%d finding(s))\n", sr.StepName, sr.Status, count)
	}
	return b.String()
}
