package prtemplate

import (
	"encoding/json"
	"testing"
)

func buildTestTemplate(t *testing.T) *Template {
	t.Helper()
	tmpl, err := Build(
		TitleInput{Template: "{{ ticket }}: {{ subject }}"},
		[]SectionInput{
			{ID: "whats_changed", Heading: "What's Changed", Required: true, MaxChars: 1500, Content: "{{ bullets }}"},
			{ID: "sister_prs", Heading: "Sister PRs", Required: false, MaxChars: 300, Content: "{{ sister prs }}"},
			{ID: "pipeline", Source: "pipeline.summary"},
		},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return tmpl
}

func TestSchema_OneNullablePropertyPerSlot(t *testing.T) {
	tmpl := buildTestTemplate(t)
	raw := BuildSchema(tmpl)
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	wantKeys := []string{"title_1", "title_2", "whats_changed_1", "sister_prs_1"}
	if len(doc.Properties) != len(wantKeys) {
		t.Fatalf("properties = %v, want keys %v", doc.Properties, wantKeys)
	}
	for _, key := range wantKeys {
		prop, ok := doc.Properties[key]
		if !ok {
			t.Fatalf("missing property %q", key)
		}
		if len(prop.Type) != 2 || prop.Type[0] != "string" || prop.Type[1] != "null" {
			t.Fatalf("property %q type = %v, want [string null]", key, prop.Type)
		}
	}
}

func TestSchema_AllSlotsAreSchemaRequired_NoAdditionalProperties(t *testing.T) {
	tmpl := buildTestTemplate(t)
	raw := BuildSchema(tmpl)
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if doc.AdditionalProperties {
		t.Fatal("additionalProperties must be false")
	}
	if len(doc.Required) != len(doc.Properties) {
		t.Fatalf("required = %v, properties = %v; every key must be schema-required", doc.Required, doc.Properties)
	}
	for key := range doc.Properties {
		found := false
		for _, r := range doc.Required {
			if r == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("property %q is not in required list %v", key, doc.Required)
		}
	}
}

func TestSchema_BriefBecomesPropertyDescription(t *testing.T) {
	tmpl := buildTestTemplate(t)
	raw := BuildSchema(tmpl)
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if doc.Properties["title_1"].Description != "ticket" {
		t.Fatalf("title_1 description = %q", doc.Properties["title_1"].Description)
	}
	if doc.Properties["whats_changed_1"].MaxLength != 1500 {
		t.Fatalf("whats_changed_1 maxLength = %d, want 1500", doc.Properties["whats_changed_1"].MaxLength)
	}
}
