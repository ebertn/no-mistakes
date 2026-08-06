package prtemplate

import "encoding/json"

// schemaProperty is one JSON-schema property for a single slot.
type schemaProperty struct {
	Type        []string `json:"type"`
	MaxLength   int      `json:"maxLength"`
	Description string   `json:"description"`
}

// schemaDoc is the JSON schema derived from a Template: one property per
// slot, additionalProperties: false.
type schemaDoc struct {
	Type                 string                    `json:"type"`
	AdditionalProperties bool                      `json:"additionalProperties"`
	Properties           map[string]schemaProperty `json:"properties"`
	Required             []string                  `json:"required"`
}

// BuildSchema derives the structured-output JSON schema from tmpl: one
// nullable-string property per slot, its brief as the description, and every
// key schema-required so the agent must address every placeholder. Every
// slot's type stays nullable (["string","null"]) regardless of whether its
// owning title or section is `required:` - a required slot may still decline
// with null; required-ness is enforced downstream (a single re-ask, then the
// PR step fails), never by forbidding the answer the agent is explicitly
// licensed to give.
func BuildSchema(tmpl *Template) json.RawMessage {
	doc := schemaDoc{
		Type:                 "object",
		AdditionalProperties: false,
		Properties:           map[string]schemaProperty{},
	}

	addSlot := func(slot *Slot, maxChars int) {
		if maxChars <= 0 {
			maxChars = DefaultPRSlotChars
		}
		doc.Properties[slot.Key] = schemaProperty{
			Type:        []string{"string", "null"},
			MaxLength:   maxChars,
			Description: slot.Brief,
		}
		doc.Required = append(doc.Required, slot.Key)
	}

	titleMax := tmpl.Title.MaxChars
	if titleMax <= 0 {
		titleMax = MaxPRTitleChars
	}
	for i := range tmpl.Title.Segments {
		if slot := tmpl.Title.Segments[i].Slot; slot != nil {
			addSlot(slot, titleMax)
		}
	}
	for _, section := range tmpl.Sections {
		for i := range section.Segments {
			if slot := section.Segments[i].Slot; slot != nil {
				addSlot(slot, section.MaxChars)
			}
		}
	}

	payload, err := json.Marshal(doc)
	if err != nil {
		// doc is built entirely from this package's own types; marshaling
		// cannot fail in practice.
		return json.RawMessage(`{"type":"object"}`)
	}
	return json.RawMessage(payload)
}
