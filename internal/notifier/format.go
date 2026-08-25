package notifier

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxSnippetRunes = 300

func Summary(match Match, includeSnippet bool) string {
	parts := []string{
		fmt.Sprintf("Baraholler rule %q matched.", match.RuleName),
		"Source: " + match.SourceName,
		"Matched terms: " + strings.Join(match.MatchedTerms, ", "),
	}
	if includeSnippet {
		if snippet := Snippet(match.Text); snippet != "" {
			parts = append(parts, "Message: "+snippet)
		}
		if link := MessageLink(match.SourceURL, match.MessageID); link != "" {
			parts = append(parts, "Open: "+link)
		}
	}
	return strings.Join(parts, "\n")
}

func MessageLink(sourceURL string, messageID int) string {
	return strings.TrimRight(sourceURL, "/") + fmt.Sprintf("/%d", messageID)
}

func Snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= maxSnippetRunes {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:maxSnippetRunes])) + "…"
}
