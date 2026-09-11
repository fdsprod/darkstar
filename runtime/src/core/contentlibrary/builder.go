package contentlibrary

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type PromptSectionEvidence struct {
	ID       string `json:"id"`
	Included bool   `json:"included"`
	Reason   string `json:"reason"`
}

// PromptPreview contains instructions only. Input values are supplied separately,
// once under their declared names, and are never interpolated into instructions.
type PromptPreview struct {
	Instructions        string                  `json:"instructions"`
	Sections            []PromptSectionEvidence `json:"sections"`
	EstimatedTokens     int                     `json:"estimatedTokens"`
	TokenEstimateMethod string                  `json:"tokenEstimateMethod"`
}

func BuildPrompt(document Document, linkedInputs []string, revision bool) (PromptPreview, error) {
	if err := document.Validate(); err != nil {
		return PromptPreview{}, err
	}
	if document.Kind != "prompt" {
		return PromptPreview{}, errors.New("prompt builder requires a prompt document")
	}
	linked := make(map[string]bool, len(linkedInputs))
	for _, input := range linkedInputs {
		if strings.TrimSpace(input) == "" {
			return PromptPreview{}, errors.New("linked input name is required")
		}
		linked[input] = true
	}
	result := PromptPreview{Sections: []PromptSectionEvidence{}, TokenEstimateMethod: "characters_divided_by_four"}
	parts := []string{document.Instructions}
	for _, section := range document.Sections {
		included := false
		reason := ""
		switch section.When.Kind {
		case "always":
			included, reason = true, "Always included"
		case "input_linked":
			included = linked[section.When.Input]
			reason = fmt.Sprintf("Input %s connected: %t", section.When.Input, included)
		case "input_absent":
			included = !linked[section.When.Input]
			reason = fmt.Sprintf("Input %s not connected: %t", section.When.Input, included)
		case "revision":
			included = revision
			reason = fmt.Sprintf("Revision attempt: %t", revision)
		default:
			return PromptPreview{}, fmt.Errorf("unsupported prompt condition %q", section.When.Kind)
		}
		result.Sections = append(result.Sections, PromptSectionEvidence{ID: section.ID, Included: included, Reason: reason})
		if included {
			parts = append(parts, section.Instructions)
		}
	}
	result.Instructions = strings.Join(parts, "\n\n")
	result.EstimatedTokens = (utf8.RuneCountInString(result.Instructions) + 3) / 4
	return result, nil
}
