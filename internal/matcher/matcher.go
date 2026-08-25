package matcher

import (
	"strings"

	"golang.org/x/text/cases"
)

type Term struct {
	Original   string
	normalized string
}

type Matcher struct {
	terms []Term
}

func New(terms []string) Matcher {
	folder := cases.Fold()
	result := make([]Term, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		original := strings.TrimSpace(term)
		normalized := folder.String(original)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, Term{Original: original, normalized: normalized})
	}
	return Matcher{terms: result}
}

func (m Matcher) Match(text string) []string {
	normalized := cases.Fold().String(text)
	matched := make([]string, 0, len(m.terms))
	for _, term := range m.terms {
		if strings.Contains(normalized, term.normalized) {
			matched = append(matched, term.Original)
		}
	}
	return matched
}
