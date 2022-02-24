package anchor

import (
	"testing"

	. "github.com/onsi/gomega"
	k8sadm "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/foresttest"
)

func TestCreateSubnamespaces(t *testing.T) {
	// Creat namespace "a" as the root with one subnamespace "b" and one full child
	// namespace "c".
	f := foresttest.Create("-Aa")
	v := &Validator{Forest: f}
	config.SetNamespaces("", "kube-system")

	tests := []struct {
		name string
		pnm  string
		cnm  string
		fail bool
	}{
		{name: "with a non-existing name", pnm: "a", cnm: "brumpf"},
		{name: "in excluded ns", pnm: "kube-system", cnm: "brumpf", fail: true},
		{name: "using excluded ns name", pnm: "brumpf", cnm: "kube-system", fail: true},
		{name: "with an existing ns name (the ns is not a subnamespace of it)", pnm: "c", cnm: "b", fail: true},
		{name: "for existing non-subns child", pnm: "a", cnm: "c", fail: true},
		{name: "for existing subns", pnm: "a", cnm: "b"},
		{name: "for non DNS label compliant child", pnm: "a", cnm: "child.01", fail: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			g := NewWithT(t)
			anchor := &api.SubnamespaceAnchor{}
			anchor.ObjectMeta.Namespace = tc.pnm
			anchor.ObjectMeta.Name = tc.cnm
			req := &anchorRequest{
				anchor: anchor,
				op:     k8sadm.Create,
			}

			// Test
			got := v.handle(req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}

func TestManagedMeta(t *testing.T) {
	f := foresttest.Create("-") // a
	v := &Validator{Forest: f}
	config.SetNamespaces("", "kube-system")
	// For this test we accept any label or annotation not starting with 'h',
	// to allow almost any meta - except the hnc.x-k8s.io labels/annotations,
	// which cannot be managed anyway. And allows us to use that for testing.
	if err := config.SetManagedMeta([]string{"[^h].*"}, []string{"[^h].*"}); err != nil {
		t.Fatal(err)
	}
	defer config.SetManagedMeta(nil, nil)

	tests := []struct {
		name        string
		labels      []api.MetaKVP
		annotations []api.MetaKVP
		allowed     bool
	}{
		{name: "ok: managed label", labels: []api.MetaKVP{{Key: "label.com/team"}}, allowed: true},
		{name: "invalid: unmanaged label", labels: []api.MetaKVP{{Key: api.LabelIncludedNamespace}}},
		{name: "ok: managed annotation", annotations: []api.MetaKVP{{Key: "annot.com/log-index"}}, allowed: true},
		{name: "invalid: unmanaged annotation", annotations: []api.MetaKVP{{Key: api.AnnotationManagedBy}}},

		{name: "ok: prefixed label key", labels: []api.MetaKVP{{Key: "foo.bar/team", Value: "v"}}, allowed: true},
		{name: "ok: bare label key", labels: []api.MetaKVP{{Key: "team", Value: "v"}}, allowed: true},
		{name: "invalid: label prefix key", labels: []api.MetaKVP{{Key: "foo;bar/team", Value: "v"}}},
		{name: "invalid: label name key", labels: []api.MetaKVP{{Key: "foo.bar/-team", Value: "v"}}},
		{name: "invalid: empty label key", labels: []api.MetaKVP{{Key: "", Value: "v"}}},

		{name: "ok: label value", labels: []api.MetaKVP{{Key: "k", Value: "foo"}}, allowed: true},
		{name: "ok: empty label value", labels: []api.MetaKVP{{Key: "k", Value: ""}}, allowed: true},
		{name: "ok: label value special char", labels: []api.MetaKVP{{Key: "k", Value: "f-oo"}}, allowed: true},
		{name: "invalid: label value", labels: []api.MetaKVP{{Key: "k", Value: "-foo"}}},

		{name: "ok: prefixed annotation key", annotations: []api.MetaKVP{{Key: "foo.bar/team", Value: "v"}}, allowed: true},
		{name: "ok: bare annotation key", annotations: []api.MetaKVP{{Key: "team", Value: "v"}}, allowed: true},
		{name: "invalid: annotation prefix key", annotations: []api.MetaKVP{{Key: "foo;bar/team", Value: "v"}}},
		{name: "invalid: annotation name key", annotations: []api.MetaKVP{{Key: "foo.bar/-team", Value: "v"}}},
		{name: "invalid: empty annotation key", annotations: []api.MetaKVP{{Key: "", Value: "v"}}},

		{name: "ok: annotation value", annotations: []api.MetaKVP{{Key: "k", Value: "foo"}}, allowed: true},
		{name: "ok: empty annotation value", annotations: []api.MetaKVP{{Key: "k", Value: ""}}, allowed: true},
		{name: "ok: special annotation value", annotations: []api.MetaKVP{{Key: "k", Value: ";$+:;/*'\""}}, allowed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			g := NewWithT(t)

			anchor := &api.SubnamespaceAnchor{}
			anchor.ObjectMeta.Namespace = "a"
			anchor.ObjectMeta.Name = "brumpf"
			anchor.Spec.Labels = tc.labels
			anchor.Spec.Annotations = tc.annotations

			req := &anchorRequest{
				anchor: anchor,
				op:     k8sadm.Create,
			}

			// Test
			got := v.handle(req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).Should(Equal(tc.allowed))
		})
	}
}

func TestAllowCascadingDeleteSubnamespaces(t *testing.T) {
	// Create a chain of namespaces from "a" to "e", with "a" as the root. Among them,
	// "b", "d" and "e" are subnamespaces. This is set up in a long chain to test that
	// subnamespaces will look all the way up to get the 'allowCascadingDeletion` value
	// and won't stop looking when the first full namespace ancestor is met.
	f := foresttest.Create("-AbCD")
	v := &Validator{Forest: f}

	tests := []struct {
		name string
		acd  string
		pnm  string
		cnm  string
		stt  api.SubnamespaceAnchorState // anchor state, "ok" by default
		fail bool
	}{
		{name: "set in parent", acd: "c", pnm: "c", cnm: "d"},
		{name: "set in non-leaf", acd: "d", pnm: "c", cnm: "d"},
		{name: "set in ancestor that is not the first full namespace", acd: "a", pnm: "c", cnm: "d"},
		{name: "unset in leaf", pnm: "d", cnm: "e"},
		{name: "unset in non-leaf", pnm: "c", cnm: "d", fail: true},
		{name: "unset in non-leaf but bad anchor (incorrect hierarchy)", pnm: "b", cnm: "d", stt: api.Conflict},
		{name: "unset in non-leaf but bad anchor (correct hierarchy)", pnm: "c", cnm: "d", stt: api.Conflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.acd != "" {
				f.Get(tc.acd).UpdateAllowCascadingDeletion(true)
				defer f.Get(tc.acd).UpdateAllowCascadingDeletion(false)
			}

			// Setup
			g := NewWithT(t)
			anchor := &api.SubnamespaceAnchor{}
			anchor.ObjectMeta.Namespace = tc.pnm
			anchor.ObjectMeta.Name = tc.cnm
			if tc.stt == "" {
				tc.stt = api.Ok
			}
			anchor.Status.State = tc.stt
			req := &anchorRequest{
				anchor: anchor,
				op:     k8sadm.Delete,
			}

			// Test
			got := v.handle(req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}

func logResult(t *testing.T, result *metav1.Status) {
	t.Logf("Got reason %q, code %d, msg %q", result.Reason, result.Code, result.Message)
}
