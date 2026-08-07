package prtemplate

import (
	"strings"
	"testing"
)

func floorSection() SectionInput {
	return SectionInput{ID: "pipeline", Source: "pipeline.summary"}
}

func TestValidate_RejectsMissingPipelineFloor(t *testing.T) {
	_, err := Build(TitleInput{}, []SectionInput{
		{ID: "whats_changed", Content: "{{ bullets }}"},
	})
	if err == nil || !strings.Contains(err.Error(), "evidence floor") {
		t.Fatalf("expected missing-floor error, got %v", err)
	}
}

func TestValidate_AcceptsPipelineOrPipelineSummaryAsFloor(t *testing.T) {
	for _, source := range []string{"pipeline", "pipeline.summary"} {
		t.Run(source, func(t *testing.T) {
			_, err := Build(TitleInput{}, []SectionInput{{ID: "pipeline", Source: source}})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
		})
	}
}

func TestValidate_RejectsDuplicateSectionID(t *testing.T) {
	_, err := Build(TitleInput{}, []SectionInput{
		{ID: "dup", Content: "{{ a }}"},
		{ID: "dup", Content: "{{ b }}"},
		floorSection(),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}
}

func TestValidate_RejectsBothContentAndSource(t *testing.T) {
	_, err := Build(TitleInput{}, []SectionInput{
		{ID: "x", Content: "{{ a }}", Source: "pipeline.risk"},
		floorSection(),
	})
	if err == nil || !strings.Contains(err.Error(), "never both") {
		t.Fatalf("expected both-content-and-source error, got %v", err)
	}
}

func TestValidate_RejectsNeitherContentNorSource(t *testing.T) {
	_, err := Build(TitleInput{}, []SectionInput{
		{ID: "x"},
		floorSection(),
	})
	if err == nil {
		t.Fatal("expected error for section with neither content nor source")
	}
}

func TestValidate_RejectsUnknownSource(t *testing.T) {
	_, err := Build(TitleInput{}, []SectionInput{
		{ID: "x", Source: "pipeline.bogus"},
		floorSection(),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("expected unknown-source error, got %v", err)
	}
}

func TestValidate_BoundsSectionsSlotsAndBytes(t *testing.T) {
	t.Run("too many sections", func(t *testing.T) {
		sections := []SectionInput{floorSection()}
		for i := 0; i < MaxPRSections+1; i++ {
			sections = append(sections, SectionInput{ID: "s", Content: "{{ x }}"})
		}
		if _, err := Build(TitleInput{}, sections); err == nil {
			t.Fatal("expected too-many-sections error")
		}
	})

	t.Run("too many slots in one section", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i <= MaxPRSlotsPerSection; i++ {
			b.WriteString("{{ slot }} ")
		}
		_, err := Build(TitleInput{}, []SectionInput{
			{ID: "s", Content: b.String()},
			floorSection(),
		})
		if err == nil {
			t.Fatal("expected too-many-slots error")
		}
	})

	t.Run("brief too long", func(t *testing.T) {
		brief := strings.Repeat("a", MaxPRSlotBriefBytes+1)
		_, err := Build(TitleInput{}, []SectionInput{
			{ID: "s", Content: "{{ " + brief + " }}"},
			floorSection(),
		})
		if err == nil {
			t.Fatal("expected brief-too-long error")
		}
	})

	t.Run("total template bytes too large", func(t *testing.T) {
		big := strings.Repeat("x", MaxPRTemplateBytes+1)
		_, err := Build(TitleInput{}, []SectionInput{
			{ID: "s", Content: big},
			floorSection(),
		})
		if err == nil {
			t.Fatal("expected template-too-large error")
		}
	})

	t.Run("negative max_chars rejected", func(t *testing.T) {
		_, err := Build(TitleInput{}, []SectionInput{
			{ID: "s", Content: "{{ x }}", MaxChars: -1},
			floorSection(),
		})
		if err == nil {
			t.Fatal("expected negative max_chars error")
		}
	})
}

func TestValidate_WorstCaseRenderedSizeFitsCeiling(t *testing.T) {
	_, err := Build(TitleInput{}, []SectionInput{
		{ID: "s", Content: "{{ x }}", MaxChars: MaxWorstCaseRenderedBytes},
		floorSection(),
	})
	if err == nil {
		t.Fatal("expected worst-case-size error")
	}
}

func TestValidate_RejectsUnparseableMustMatch(t *testing.T) {
	_, err := Build(TitleInput{MustMatch: "("}, nil)
	if err == nil {
		t.Fatal("expected must_match compile error")
	}
}

func TestBuild_TitleOnlyWithNoSectionsSkipsFloorRequirement(t *testing.T) {
	tmpl, err := Build(TitleInput{MustMatch: "^[A-Z]+-[0-9]+: .+"}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if tmpl.HasSections() {
		t.Fatal("expected no sections for a title-only template")
	}
	if tmpl.Title.MustMatch == nil {
		t.Fatal("expected must_match to compile even with no template text and no sections")
	}
}

func TestBuild_ConventionalDefaultsFalseWhenTemplateSetTrueOtherwise(t *testing.T) {
	withTemplate, err := Build(TitleInput{Template: "{{ x }}"}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if withTemplate.Title.Conventional {
		t.Fatal("expected Conventional to default false when a title template is set")
	}

	withoutTemplate, err := Build(TitleInput{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !withoutTemplate.Title.Conventional {
		t.Fatal("expected Conventional to default true when no title template is set (today's behavior)")
	}

	explicitTrue := true
	withOverride, err := Build(TitleInput{Template: "{{ x }}", Conventional: &explicitTrue}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !withOverride.Title.Conventional {
		t.Fatal("expected an explicit conventional:true to override the presence-dependent default")
	}
}

func TestBuild_StrictDefaultsTrue(t *testing.T) {
	tmpl, err := Build(TitleInput{Template: "{{ x }}"}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !tmpl.Title.Strict {
		t.Fatal("expected Strict to default true (Decision 4)")
	}
}

func TestValidate_TitleMaxCharsBounds(t *testing.T) {
	if _, err := Build(TitleInput{MaxChars: -1}, nil); err == nil {
		t.Fatal("expected negative title max_chars error")
	}
	if _, err := Build(TitleInput{MaxChars: MaxPRTitleChars + 1}, nil); err == nil {
		t.Fatal("expected title max_chars ceiling error")
	}
}
