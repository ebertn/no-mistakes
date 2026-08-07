package github

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func TestGitHubCapabilities_DeclaresPRLabelsAndDraft(t *testing.T) {
	t.Parallel()
	host := New(nil, nil, "", "test/repo")
	caps := host.Capabilities()
	if !caps.PRLabels {
		t.Fatal("expected PRLabels capability")
	}
	if !caps.PRDraft {
		t.Fatal("expected PRDraft capability")
	}
}

func TestGitHubCreatePR_PassesDraftFlagWhenDraftSet(t *testing.T) {
	t.Parallel()

	host := New(githubTestCmdFactory(map[string]githubTestResponse{
		"gh pr create --head feature/x --base main --repo test/repo --title t --body-file - --draft": {
			stdout: "https://github.com/test/repo/pull/42\n",
		},
	}), nil, "", "test/repo")

	_, err := host.CreatePR(context.Background(), "feature/x", "main", scm.PRContent{
		Title: "t", Body: "b", Draft: true,
	})
	if err != nil {
		t.Fatalf("CreatePR() error = %v", err)
	}
}

func TestGitHubCreatePR_OmitsDraftFlagOtherwise(t *testing.T) {
	t.Parallel()

	host := New(githubTestCmdFactory(map[string]githubTestResponse{
		"gh pr create --head feature/x --base main --repo test/repo --title t --body-file -": {
			stdout: "https://github.com/test/repo/pull/42\n",
		},
	}), nil, "", "test/repo")

	_, err := host.CreatePR(context.Background(), "feature/x", "main", scm.PRContent{
		Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatalf("CreatePR() error = %v", err)
	}
}

func TestGitHubCreatePR_AppliesLabelsAfterCreation(t *testing.T) {
	t.Parallel()

	var recorded [][]string
	host := New(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		recorded = append(recorded, append([]string{name}, args...))
		stdout := ""
		if len(recorded) == 1 {
			stdout = "https://github.com/test/repo/pull/42\n"
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestGitHubHelperProcess", "--", "recorded")
		cmd.Env = append(os.Environ(),
			"GITHUB_TEST_HELPER=1",
			"GITHUB_TEST_STDOUT="+stdout,
			"GITHUB_TEST_EXIT_CODE=0",
		)
		return cmd
	}, nil, "", "test/repo")

	_, err := host.CreatePR(context.Background(), "feature/x", "main", scm.PRContent{
		Title: "t", Body: "b", Labels: []string{"automated", "needs-review"},
	})
	if err != nil {
		t.Fatalf("CreatePR() error = %v", err)
	}
	if len(recorded) != 2 {
		t.Fatalf("expected create + one label-apply invocation, got %d: %v", len(recorded), recorded)
	}
	labelArgv := strings.Join(recorded[1], " ")
	if !strings.Contains(labelArgv, "pr edit 42") {
		t.Fatalf("label argv = %q, want it to target PR #42 via prSelector", labelArgv)
	}
	if !strings.Contains(labelArgv, "--add-label automated") || !strings.Contains(labelArgv, "--add-label needs-review") {
		t.Fatalf("label argv = %q, want both labels added", labelArgv)
	}
}

// TestGitHubApplyLabels_FailureIsNonFatal proves a typo'd label (one gh
// rejects because it does not exist in the repo) does not throw away a
// completed pipeline run's PR: CreatePR still returns the created PR.
func TestGitHubApplyLabels_FailureIsNonFatal(t *testing.T) {
	t.Parallel()

	call := 0
	host := New(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		call++
		if call == 1 {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestGitHubHelperProcess", "--", "recorded")
			cmd.Env = append(os.Environ(),
				"GITHUB_TEST_HELPER=1",
				"GITHUB_TEST_STDOUT=https://github.com/test/repo/pull/42\n",
				"GITHUB_TEST_EXIT_CODE=0",
			)
			return cmd
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestGitHubHelperProcess", "--", "recorded")
		cmd.Env = append(os.Environ(),
			"GITHUB_TEST_HELPER=1",
			"GITHUB_TEST_STDERR=label 'bogus' not found",
			"GITHUB_TEST_EXIT_CODE=1",
		)
		return cmd
	}, nil, "", "test/repo")

	pr, err := host.CreatePR(context.Background(), "feature/x", "main", scm.PRContent{
		Title: "t", Body: "b", Labels: []string{"bogus"},
	})
	if err != nil {
		t.Fatalf("CreatePR() error = %v, want the PR creation to succeed despite the label failure", err)
	}
	if pr == nil || pr.Number != "42" {
		t.Fatalf("CreatePR() PR = %+v, want #42", pr)
	}
}

func TestGitHubApplyLabels_UsesPRSelectorNotABareNumber(t *testing.T) {
	t.Parallel()

	host := New(nil, nil, "", "test/repo")
	if err := host.applyLabels(context.Background(), &scm.PR{}, []string{"x"}); err == nil {
		t.Fatal("expected applyLabels to fail closed without a known PR number or URL")
	}
}
