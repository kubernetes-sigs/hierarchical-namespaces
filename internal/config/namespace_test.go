package config

import (
	. "github.com/onsi/gomega"
	"testing"
)

func TestIsNamespaceIncluded(t *testing.T) {

	tests := []struct {
		name              string
		regex             string
		excludeNamespaces []string
		expect            bool
	}{
		{name: "foobar", regex: "foo.*", excludeNamespaces: []string{"bar"}, expect: true},
		{name: "bar", regex: "foo-.*", excludeNamespaces: []string{"bar"}, expect: false},
		{name: "bar", regex: ".*", excludeNamespaces: []string{"bar"}, expect: false},
		{name: "foo", regex: ".*", excludeNamespaces: []string{"bar"}, expect: true},
		{name: "foo", regex: ".*", excludeNamespaces: []string{"bar", "foo"}, expect: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			// Test
			SetNamespaces(tc.regex, tc.excludeNamespaces...)
			isIncluded := IsNamespaceIncluded(tc.name)

			// Report
			g.Expect(isIncluded).Should(Equal(tc.expect))
		})
	}

}
