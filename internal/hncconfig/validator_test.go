package hncconfig

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	k8sadm "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/foresttest"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

var (
	// This mapping is used to implement a fake grTranslator with GVKFor() method.
	gr2gvk = map[schema.GroupResource]schema.GroupVersionKind{
		{Group: api.RBACGroup, Resource: api.RoleResource}:        {Group: api.RBACGroup, Version: "v1", Kind: api.RoleKind},
		{Group: api.RBACGroup, Resource: api.RoleBindingResource}: {Group: api.RBACGroup, Version: "v1", Kind: api.RoleBindingKind},
		{Group: "", Resource: "secrets"}:                          {Group: "", Version: "v1", Kind: "Secret"},
		{Group: "", Resource: "resourcequotas"}:                   {Group: "", Version: "v1", Kind: "ResourceQuota"},
	}
)

func TestDeletingConfigObject(t *testing.T) {
	t.Run("Delete config object", func(t *testing.T) {
		g := NewWithT(t)
		req := admission.Request{AdmissionRequest: k8sadm.AdmissionRequest{
			Operation: k8sadm.Delete,
			Name:      api.HNCConfigSingleton,
		}}
		validator := &Validator{Log: zap.New()}

		got := validator.Handle(context.Background(), req)

		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeFalse())
	})
}

func TestDeletingOtherObject(t *testing.T) {
	t.Run("Delete config object", func(t *testing.T) {
		g := NewWithT(t)
		req := admission.Request{AdmissionRequest: k8sadm.AdmissionRequest{
			Operation: k8sadm.Delete,
			Name:      "other",
		}}
		validator := &Validator{Log: zap.New()}

		got := validator.Handle(context.Background(), req)

		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeTrue())
	})
}

func TestRBACTypes(t *testing.T) {
	f := forest.NewForest()
	validator := &Validator{
		translator: fakeGRTranslator{},
		Forest:     f,
		Log:        zap.New(),
	}

	tests := []struct {
		name    string
		configs []api.ResourceSpec
		allow   bool
	}{
		{
			name:    "No redundant enforced resources configured",
			configs: []api.ResourceSpec{},
			allow:   true,
		},
		{
			name: "Configure redundant enforced resources",
			configs: []api.ResourceSpec{
				{Group: api.RBACGroup, Resource: api.RoleResource, Mode: api.Propagate},
				{Group: api.RBACGroup, Resource: api.RoleBindingResource, Mode: api.Propagate},
			},
			allow: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			c := &api.HNCConfiguration{Spec: api.HNCConfigurationSpec{Resources: tc.configs}}
			c.Name = api.HNCConfigSingleton

			got := validator.handle(c)

			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).Should(Equal(tc.allow))
		})
	}
}

func TestNonRBACTypes(t *testing.T) {
	f := fakeGRTranslator{"crontabs"}
	tests := []struct {
		name      string
		configs   []api.ResourceSpec
		validator fakeGRTranslator
		allow     bool
	}{
		{
			name: "Correct Non-RBAC resources config",
			configs: []api.ResourceSpec{
				{Group: "", Resource: "secrets", Mode: "Ignore"},
				{Group: "", Resource: "resourcequotas"},
			},
			validator: f,
			allow:     true,
		},
		{
			name: "Resource does not exist",
			configs: []api.ResourceSpec{
				// "crontabs" resource does not exist in ""
				{Group: "", Resource: "crontabs", Mode: "Ignore"},
			},
			validator: f,
			allow:     false,
		}, {
			name: "Duplicate resources with different modes",
			configs: []api.ResourceSpec{
				{Group: "", Resource: "secrets", Mode: "Ignore"},
				{Group: "", Resource: "secrets", Mode: "Propagate"},
			},
			validator: f,
			allow:     false,
		}, {
			name: "Duplicate resources with the same mode",
			configs: []api.ResourceSpec{
				{Group: "", Resource: "secrets", Mode: "Ignore"},
				{Group: "", Resource: "secrets", Mode: "Ignore"},
			},
			validator: f,
			allow:     false,
		}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			c := &api.HNCConfiguration{Spec: api.HNCConfigurationSpec{Resources: tc.configs}}
			c.Name = api.HNCConfigSingleton
			validator := &Validator{
				translator: tc.validator,
				Forest:     forest.NewForest(),
				Log:        zap.New(),
			}

			got := validator.handle(c)

			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).Should(Equal(tc.allow))
		})
	}
}

func TestPropagateConflict(t *testing.T) {
	tests := []struct {
		name   string
		forest string
		// inNamespace contains the namespaces we are creating the objects in
		inNamespace string
		// noPropagation contains the namespaces where the objects would have a noneSelector
		noPropogation string
		allow         bool
		errContain    string
	}{{
		name:        "Objects with the same name existing in namespaces that one is not an ancestor of the other would not cause overwriting conflict",
		forest:      "-aa",
		inNamespace: "bc",
		allow:       true,
	}, {
		name:        "Objects with the same name existing in namespaces that one is an ancestor of the other would have overwriting conflict",
		forest:      "-aa",
		inNamespace: "ab",
		allow:       false,
	}, {
		name:          "Should not cause a conflict if the object in the parent namespace has an exceptions selector that choose not to propagate to the conflicting child namespace",
		forest:        "-aa",
		inNamespace:   "ab",
		noPropogation: "a",
		allow:         true,
	}, {
		name:          "Should identify the real conflicting source when there are multiple conflicting sources but only one gets propagated",
		forest:        "-ab",
		inNamespace:   "abc",
		noPropogation: "a",
		allow:         false,
		errContain:    "Object \"my-creds\" in namespace \"b\" would overwrite the one in \"c\"",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			configs := []api.ResourceSpec{
				{Group: "", Resource: "secrets", Mode: "Propagate"}}
			c := &api.HNCConfiguration{Spec: api.HNCConfigurationSpec{Resources: configs}}
			c.Name = api.HNCConfigSingleton
			f := foresttest.Create(tc.forest)
			validator := &Validator{
				translator: fakeGRTranslator{},
				Forest:     f,
				Log:        zap.New(),
			}

			// Add source objects to the forest.
			for _, ns := range tc.inNamespace {
				inst := &unstructured.Unstructured{}
				inst.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"})
				inst.SetName("my-creds")
				if strings.Contains(tc.noPropogation, string(ns)) {
					inst.SetAnnotations(map[string]string{api.AnnotationNoneSelector: "true"})
				}
				f.Get(string(ns)).SetSourceObject(inst)
			}
			got := validator.handle(c)

			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).Should(Equal(tc.allow))
			if tc.errContain != "" {
				g.Expect(strings.Contains(got.AdmissionResponse.Result.Message, tc.errContain))
			}
		})
	}
}

// fakeGRTranslator implements grTranslator. Any kind that are in the slice are
// denied; anything else are translated.
type fakeGRTranslator []string

func (f fakeGRTranslator) gvkForGR(gr schema.GroupResource) (schema.GroupVersionKind, error) {
	for _, r := range f {
		if r == gr.Resource {
			return schema.GroupVersionKind{}, fmt.Errorf("%s does not exist", gr)
		}
	}
	return gr2gvk[gr], nil
}

func logResult(t *testing.T, result *metav1.Status) {
	t.Logf("Got reason %q, code %d, msg %q", result.Reason, result.Code, result.Message)
}
