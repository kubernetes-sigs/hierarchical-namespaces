package namespaces

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
)

func TestMutateNamespaceIncludedLabel(t *testing.T) {
	m := &Mutator{}
	l := zap.New()
	config.SetNamespaces("", "excluded")

	tests := []struct {
		name       string
		nsn        string
		lval       string
		expectlval string
	}{
		{name: "Set label on non-excluded namespace without label", nsn: "a", expectlval: "true"},
		{name: "No operation on non-excluded namespace with label (e.g. from a yaml file)", nsn: "a", lval: "true", expectlval: "true"},
		{name: "No operation on non-excluded namespace with label with wrong value(e.g. from a yaml file)", nsn: "a", lval: "wrong", expectlval: "wrong"},
		{name: "No operation on excluded namespace without label", nsn: "excluded"},
		{name: "No operation on excluded namespace with label (e.g. from a yaml file)", nsn: "excluded", lval: "true", expectlval: "true"},
		{name: "No operation on excluded namespace with label with wrong value (e.g. from a yaml file)", nsn: "excluded", lval: "wrong", expectlval: "wrong"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			nsInst := &corev1.Namespace{}
			nsInst.Name = tc.nsn
			if tc.lval != "" {
				nsInst.SetLabels(map[string]string{api.LabelIncludedNamespace: tc.lval})
			}

			// Test
			m.handle(l, nsInst)

			// Report
			g.Expect(nsInst.Labels[api.LabelIncludedNamespace]).Should(Equal(tc.expectlval))
		})
	}
}
