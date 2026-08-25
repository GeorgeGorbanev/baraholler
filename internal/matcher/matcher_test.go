package matcher

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatcherUsesAnyUnicodeCaseFoldedTerm(t *testing.T) {
	t.Parallel()

	value := New([]string{"MACBOOK", "Straße", "ноутбук", "macbook"})
	tests := []struct {
		text string
		want []string
	}{
		{text: "Selling a MacBook Pro", want: []string{"MACBOOK"}},
		{text: "STRASSE and НОУТБУК", want: []string{"Straße", "ноутбук"}},
		{text: "unrelated listing", want: []string{}},
	}
	for _, test := range tests {
		require.Equal(t, test.want, value.Match(test.text))
	}
}
