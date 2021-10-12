package config

import (
	. "github.com/onsi/gomega"
	"testing"
)

func TestIsNamespaceIncluded(t *testing.T) {

	tests := []struct {
		name     string
		regex    string
		excluded []string
		expect   bool
	}{
		{name: "foobar", regex: "foo.*", excluded: []string{"bar"}, expect: true},
		{name: "bar", regex: "foo-.*", excluded: []string{"bar"}, expect: false},
		{name: "bar", regex: ".*", excluded: []string{"bar"}, expect: false},
		{name: "foo", regex: ".*", excluded: []string{"bar"}, expect: true},
		{name: "foo", regex: ".*", excluded: []string{"bar", "foo"}, expect: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			// Test
			SetNamespaces(tc.regex, tc.excluded...)
			got := IsManagedNamespace(tc.name)

			// Report
			g.Expect(got).Should(Equal(tc.expect))
		})
	}

}
