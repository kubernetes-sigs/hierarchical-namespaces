package objects_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	. "sigs.k8s.io/hierarchical-namespaces/internal/integtest"
)

func TestInteg(t *testing.T) {
	HNCRun(t, "Objects reconciler")
}

var _ = BeforeSuite(HNCBeforeSuite)
var _ = AfterSuite(HNCAfterSuite)

var _ = Describe("Exceptions", func() {
	ctx := context.Background()

	BeforeEach(func() {
		// We want to ensure we're working with a clean slate, in case a previous tests objects still exist
		CleanupObjects(ctx)
	})

	AfterEach(func() {
		// Change current config back to the default value.
		ResetHNCConfigToDefault(ctx)
		CleanupObjects(ctx)
	})

	Context("Add exception annotations", func() {
		const (
			p  = "parent"
			c1 = "child1"
			c2 = "child2"
			c3 = "child3"
		)
		tests := []struct {
			name         string
			selector     string
			treeSelector string
			noneSelector string
			allSelector  string
			want         []string
			notWant      []string
		}{{
			name:     "not propagate object to a negatively selected namespace",
			selector: "!" + c1 + api.LabelTreeDepthSuffix,
			want:     []string{c2, c3},
			notWant:  []string{c1},
		}, {
			name:     "not propagate object to multiple negatively selected namespaces",
			selector: "!" + c1 + api.LabelTreeDepthSuffix + ", !" + c2 + api.LabelTreeDepthSuffix,
			want:     []string{c3},
			notWant:  []string{c1, c2},
		}, {
			// When the user input an invalid selector and we don't understand what the users' intention is,
			// we choose not to propagate this object to any child namespace to protect any object in the child
			// namespaces to be overwritten. Same for treeSelectors and noneSelector.
			name:     "not propagate to any namespace with a bad selector",
			selector: "$foo",
			want:     []string{},
			notWant:  []string{c1, c2, c3},
		}, {
			name:         "not propagate object to a negatively treeSelected namespace",
			treeSelector: "!" + c1,
			want:         []string{c2, c3},
			notWant:      []string{c1},
		}, {
			name:         "not propagate object to multiple negatively treeSelected namespaces",
			treeSelector: "!" + c1 + ", !" + c2,
			want:         []string{c3},
			notWant:      []string{c1, c2},
		}, {
			name:         "not propagate to any namespace with a bad treeSelector",
			treeSelector: "$foo",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:         "not propagate object to neither negatively selected or treeSelected namespaces",
			selector:     "!" + c1 + api.LabelTreeDepthSuffix,
			treeSelector: "!" + c2,
			want:         []string{c3},
			notWant:      []string{c1, c2},
		}, {
			name:         "only propagate object to the intersection of selected and treeSelected namespaces",
			selector:     c1 + api.LabelTreeDepthSuffix,
			treeSelector: c2,
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:         "not propagate to any object when noneSelector is set to true",
			noneSelector: "true",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:         "propagate to all objects when noneSelector is set to false",
			noneSelector: "false",
			want:         []string{c1, c2, c3},
			notWant:      []string{},
		}, {
			name:         "not propagate to any child namespace with a bad noneSelector",
			noneSelector: "$foo",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:         "only propagate the intersection of three selectors",
			selector:     c1 + api.LabelTreeDepthSuffix,
			treeSelector: c1 + ", " + c2,
			noneSelector: "true",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:         "not propagate when allSelector and noneSelector are both true",
			noneSelector: "true",
			allSelector:  "all",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:         "only propagate the intersection of four selectors",
			selector:     c1 + api.LabelTreeDepthSuffix,
			treeSelector: c1 + ", " + c2,
			noneSelector: "true",
			allSelector:  "true",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}}

		for _, tc := range tests {
			// Making a local copy of tc is necessary to ensure the correct value is passed to the closure,
			// for more details look at https://onsi.github.io/ginkgo/ and search for 'closure'
			tc := tc
			It("Should "+tc.name, func() {
				// Set up namespaces
				names := map[string]string{
					p:  CreateNS(ctx, p),
					c1: CreateNS(ctx, c1),
					c2: CreateNS(ctx, c2),
					c3: CreateNS(ctx, c3),
				}
				SetParent(ctx, names[c1], names[p])
				SetParent(ctx, names[c2], names[p])
				SetParent(ctx, names[c3], names[p])

				tc.selector = ReplaceStrings(tc.selector, names)
				tc.treeSelector = ReplaceStrings(tc.treeSelector, names)

				// Create a Role with the selector and treeSelector annotation
				MakeObjectWithAnnotations(ctx, api.RoleResource, names[p], "testrole", map[string]string{
					api.AnnotationSelector:     tc.selector,
					api.AnnotationTreeSelector: tc.treeSelector,
					api.AnnotationNoneSelector: tc.noneSelector,
					api.AnnotationAllSelector:  tc.allSelector,
				})
				for _, ns := range tc.want {
					ns = ReplaceStrings(ns, names)
					Eventually(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeTrue(), "When propagating testrole to %s", ns)
				}
				for _, ns := range tc.notWant {
					ns = ReplaceStrings(ns, names)
					Consistently(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeFalse(), "When propagating testrole to %s", ns)
				}
			})
		}
	})

	Context("Update exception annotations", func() {
		const (
			p  = "parent"
			c1 = "child1"
			c2 = "child2"
			c3 = "child3"
		)
		tests := []struct {
			name         string
			selector     string
			treeSelector string
			noneSelector string
			allSelector  string
			want         []string
			notWant      []string
		}{{
			name:     "update select",
			selector: "!" + c1 + api.LabelTreeDepthSuffix,
			want:     []string{c2, c3},
			notWant:  []string{c1},
		}, {
			name:         "update treeSelect",
			treeSelector: "!" + c1,
			want:         []string{c2, c3},
			notWant:      []string{c1},
		}, {
			name:         "update noneSelector",
			noneSelector: "true",
			want:         []string{},
			notWant:      []string{c1, c2, c3},
		}, {
			name:        "update allSelector",
			allSelector: "true",
			want:        []string{c1, c2, c3},
			notWant:     []string{},
		}}

		for _, tc := range tests {
			// Making a local copy of tc is necessary to ensure the correct value is passed to the closure,
			// for more details look at https://onsi.github.io/ginkgo/ and search for 'closure'
			tc := tc
			It("Should "+tc.name, func() {
				// Set up namespaces
				names := map[string]string{
					p:  CreateNS(ctx, p),
					c1: CreateNS(ctx, c1),
					c2: CreateNS(ctx, c2),
					c3: CreateNS(ctx, c3),
				}
				SetParent(ctx, names[c1], names[p])
				SetParent(ctx, names[c2], names[p])
				SetParent(ctx, names[c3], names[p])
				tc.selector = ReplaceStrings(tc.selector, names)
				tc.treeSelector = ReplaceStrings(tc.treeSelector, names)
				tc.allSelector = ReplaceStrings(tc.allSelector, names)

				// Create a Role and verify it's propagated
				MakeObject(ctx, api.RoleResource, names[p], "testrole")
				for _, ns := range names {
					Eventually(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeTrue(), "When propagating testrole to %s", ns)
				}

				// update the role with the selector and treeSelector annotation
				UpdateObjectWithAnnotations(ctx, api.RoleResource, names[p], "testrole", map[string]string{
					api.AnnotationSelector:     tc.selector,
					api.AnnotationTreeSelector: tc.treeSelector,
					api.AnnotationNoneSelector: tc.noneSelector,
					api.AnnotationAllSelector:  tc.allSelector,
				})
				// make sure the changes are propagated
				for _, ns := range tc.notWant {
					ns = ReplaceStrings(ns, names)
					Eventually(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeFalse(), "When propagating testrole to %s", ns)
				}
				// then check that the objects are kept in these namespaces
				for _, ns := range tc.want {
					ns = ReplaceStrings(ns, names)
					Consistently(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeTrue(), "When propagating testrole to %s", ns)
				}

				// remove the annotation and verify that the object is back for every namespace
				UpdateObjectWithAnnotations(ctx, api.RoleResource, names[p], "testrole", map[string]string{})
				for _, ns := range names {
					Eventually(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeTrue(), "When propagating testrole to %s", ns)
				}
			})
		}
	})

	Context("Update the descendant namespaces after 'select' exception annotation is set", func() {
		const (
			label        = "propagate-label"
			p            = "parent"
			labeledchild = "labeledchild"
			nolabelchild = "nolabelchild"
			labeledns    = "labeledns"
			nolabelns    = "nolabelns"
		)
		tests := []struct {
			name       string
			toLabel    string
			toUnlabel  string
			toAddChild string
			want       []string
			notWant    []string
		}{{
			name:    "propagate object only to children with the label",
			want:    []string{labeledchild},
			notWant: []string{nolabelchild},
		}, {
			name:    "propagate object to a newly-labeled child - issue #1448",
			toLabel: nolabelchild,
			want:    []string{labeledchild, nolabelchild},
			notWant: []string{},
		}, {
			name:      "not propagate object to a newly-unlabeled child - issue #1448",
			toUnlabel: labeledchild,
			want:      []string{},
			notWant:   []string{labeledchild, nolabelchild},
		}, {
			name:       "propagate object to a new child with the label",
			toAddChild: labeledns,
			want:       []string{labeledchild, labeledns},
			notWant:    []string{nolabelchild},
		}, {
			name:       "not propagate object to a new child without the label",
			toAddChild: nolabelns,
			want:       []string{labeledchild},
			notWant:    []string{nolabelchild, nolabelns},
		}}

		for _, tc := range tests {
			// Making a local copy of tc is necessary to ensure the correct value is passed to the closure,
			// for more details look at https://onsi.github.io/ginkgo/ and search for 'closure'
			tc := tc
			It("Should "+tc.name, func() {
				// Set up namespaces
				names := map[string]string{
					p:            CreateNS(ctx, p),
					labeledchild: CreateNSWithLabel(ctx, labeledchild, map[string]string{label: "true"}),
					nolabelchild: CreateNS(ctx, nolabelchild),
					labeledns:    CreateNSWithLabel(ctx, labeledns, map[string]string{label: "true"}),
					nolabelns:    CreateNS(ctx, nolabelns),
				}
				SetParent(ctx, names[labeledchild], names[p])
				SetParent(ctx, names[nolabelchild], names[p])

				// Create a Role and verify it's propagated to all children.
				MakeObject(ctx, api.RoleResource, names[p], "testrole")
				Eventually(HasObject(ctx, api.RoleResource, names[labeledchild], "testrole")).Should(BeTrue(), "When propagating testrole to %s", names[labeledchild])
				Eventually(HasObject(ctx, api.RoleResource, names[nolabelchild], "testrole")).Should(BeTrue(), "When propagating testrole to %s", names[nolabelchild])
				// Add `select` exception annotation with propagate label and verify the
				// object is only propagated to children with the label.
				UpdateObjectWithAnnotations(ctx, api.RoleResource, names[p], "testrole", map[string]string{
					api.AnnotationSelector: label,
				})
				Eventually(HasObject(ctx, api.RoleResource, names[nolabelchild], "testrole")).Should(BeFalse(), "When propagating testrole to %s", names[nolabelchild])
				Consistently(HasObject(ctx, api.RoleResource, names[nolabelchild], "testrole")).Should(BeFalse(), "When propagating testrole to %s", names[nolabelchild])
				Consistently(HasObject(ctx, api.RoleResource, names[labeledchild], "testrole")).Should(BeTrue(), "When propagating testrole to %s", names[labeledchild])

				// Add the label to the namespace if the value is not empty.
				if tc.toLabel != "" {
					AddNamespaceLabel(ctx, names[tc.toLabel], label, "true")
				}

				// Unlabel the namespace if the value is not empty.
				if tc.toUnlabel != "" {
					RemoveNamespaceLabel(ctx, names[tc.toUnlabel], label)
				}

				// Set a new child if the value is not empty.
				if tc.toAddChild != "" {
					SetParent(ctx, names[tc.toAddChild], names[p])
				}

				// then check that the objects are kept in these namespaces
				for _, ns := range tc.want {
					ns = ReplaceStrings(ns, names)
					Eventually(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeTrue(), "When propagating testrole to %s", ns)
				}
				// make sure the changes are propagated
				for _, ns := range tc.notWant {
					ns = ReplaceStrings(ns, names)
					Eventually(HasObject(ctx, api.RoleResource, ns, "testrole")).Should(BeFalse(), "When propagating testrole to %s", ns)
				}
			})
		}
	})
})

var _ = Describe("Basic propagation", func() {
	ctx := context.Background()

	var (
		fooName string
		barName string
		bazName string
	)

	BeforeEach(func() {
		fooName = CreateNS(ctx, "foo")
		barName = CreateNS(ctx, "bar")
		bazName = CreateNS(ctx, "baz")

		// We want to ensure we're working with a clean slate, in case a previous tests objects still exist
		CleanupObjects(ctx)

		// Give them each a role.
		MakeObject(ctx, api.RoleResource, fooName, "foo-role")
		MakeObject(ctx, api.RoleResource, barName, "bar-role")
		MakeObject(ctx, api.RoleResource, bazName, "baz-role")

		// This is empty by default.
		config.UnpropagatedAnnotations = nil
	})

	AfterEach(func() {
		ResetHNCConfigToDefault(ctx)
		CleanupObjects(ctx)
	})

	It("should be copied to descendents", func() {
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)

		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role")).Should(Equal(fooName))

		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "foo-role")).Should(Equal(fooName))

		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "bar-role")).Should(Equal(barName))
	})

	It("should be copied to descendents when source object is empty", func() {
		SetParent(ctx, barName, fooName)
		// Creates an empty ConfigMap. We use ConfigMap for this test because the apiserver will not
		// add additional fields to an empty ConfigMap object to make it non-empty.
		MakeObject(ctx, "configmaps", fooName, "foo-config")
		AddToHNCConfig(ctx, "", "configmaps", api.Propagate)

		// "foo-config" should now be propagated from foo to bar.
		Eventually(HasObject(ctx, "configmaps", barName, "foo-config")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "configmaps", barName, "foo-config")).Should(Equal(fooName))
	})

	It("should not propagate builtin exclusions by name", func() {
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "configmaps", fooName, "istio-ca-root-cert")
		MakeObject(ctx, "configmaps", fooName, "kube-root-ca.crt")
		MakeObject(ctx, "configmaps", fooName, "gets-propagated")
		AddToHNCConfig(ctx, "", "configmaps", api.Propagate)

		// We expect normal configmaps to be propagated, but builtin exclusions not to be.
		Eventually(HasObject(ctx, "configmaps", barName, "gets-propagated")).Should(BeTrue())
		Eventually(HasObject(ctx, "configmaps", barName, "istio-ca-root-cert")).Should(BeFalse())
		Eventually(HasObject(ctx, "configmaps", barName, "kube-root-ca.crt")).Should(BeFalse())
	})

	It("should not propagate builtin exclusions by Secret Type", func() {
		SetParent(ctx, barName, fooName)
		MakeSecrettWithType(ctx, fooName, "gets-propagated", "Opaque")
		MakeSecrettWithType(ctx, fooName, "helm-secret", "helm.sh/release.v1")
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)

		// We expect normal secrets to be propagated, but builtin exclusions not to be.
		Eventually(HasObject(ctx, "secrets", barName, "gets-propagated")).Should(BeTrue())
		Eventually(HasObject(ctx, "secrets", barName, "helm-secret")).Should(BeFalse())
	})

	It("should not propagate objects excluded by labels", func() {
		SetParent(ctx, barName, fooName)
		config.NoPropagationLabels = append(config.NoPropagationLabels, config.NoPropagationLabel{Key: "cattle.io/creator", Value: "norman"})
		config.NoPropagationLabels = append(config.NoPropagationLabels, config.NoPropagationLabel{Key: "ignore-label", Value: "label"})

		MakeObjectWithLabels(ctx, "roles", fooName, "role-with-labels-blocked", map[string]string{"cattle.io/creator": "norman"})
		MakeObjectWithLabels(ctx, "roles", fooName, "role-with-labels-blocked-2", map[string]string{"ignore-label": "label"})
		MakeObjectWithLabels(ctx, "roles", fooName, "role-with-labels-wrong-value", map[string]string{"cattle.io/creator": "testme"})
		MakeObjectWithLabels(ctx, "roles", fooName, "role-with-labels-something", map[string]string{"app": "hello-world"})
		AddToHNCConfig(ctx, api.RBACGroup, api.RoleKind, api.Propagate)

		// the first one should not propagate, everything else should
		Consistently(HasObject(ctx, "roles", barName, "role-with-labels-blocked")).Should(BeFalse())
		Consistently(HasObject(ctx, "roles", barName, "role-with-labels-blocked-2")).Should(BeFalse())
		Eventually(HasObject(ctx, "roles", barName, "role-with-labels-wrong-value")).Should(BeTrue())
		Eventually(HasObject(ctx, "roles", barName, "role-with-labels-something")).Should(BeTrue())

		// lets try the same with config maps, they are also filtered and should not propagate
		MakeObjectWithLabels(ctx, "configmaps", fooName, "cm-with-label-1", map[string]string{"cattle.io/creator": "norman"})
		MakeObjectWithLabels(ctx, "configmaps", fooName, "cm-with-label-2", map[string]string{"app": "hello-world"})
		AddToHNCConfig(ctx, "", "configmaps", api.Propagate)

		Consistently(HasObject(ctx, "configmaps", barName, "cm-with-label-1")).Should(BeFalse())
		Eventually(HasObject(ctx, "configmaps", barName, "cm-with-label-2")).Should(BeTrue())
	})

	It("should not propagate objects excluded by labels when using all annotation", func() {
		SetParent(ctx, barName, fooName)
		config.NoPropagationLabels = append(config.NoPropagationLabels, config.NoPropagationLabel{Key: "cattle.io/creator", Value: "norman"})

		MakeObjectWithLabels(ctx, "roles", fooName, "role-with-labels-blocked", map[string]string{"cattle.io/creator": "norman"})
		UpdateObjectWithAnnotations(ctx, "roles", fooName, "role-with-labels-blocked", map[string]string{"propagate.hnc.x-k8s.io/all": "true"})
		AddToHNCConfig(ctx, api.RBACGroup, api.RoleKind, api.Propagate)

		// The object should not be propagated even though it uses the `all` annotation
		Consistently(HasObject(ctx, "roles", barName, "role-with-labels-blocked")).Should(BeFalse())
	})

	It("should be removed if the hierarchy changes", func() {
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())
		SetParent(ctx, bazName, fooName)
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		SetParent(ctx, bazName, "")
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeFalse())
	})

	It("should not be propagated if modified", func() {
		// Set tree as bar -> foo and make sure the first-time propagation of foo-role
		// is finished before modifying the foo-role in bar namespace
		SetParent(ctx, barName, fooName)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())

		// Wait 1 second to make sure all enqueued fooName hiers are successfully reconciled
		// in case the manual modification is overridden by the unfinished propagation.
		time.Sleep(1 * time.Second)
		modifyRole(ctx, barName, "foo-role")

		// Set as parent. Give the reconciler a chance to copy the objects and make
		// sure that at least the correct one was copied. This gives us more confidence
		// that if the other one *isn't* copied, this is because we decided not to, and
		// not that we just haven't gotten to it yet.
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())

		// Make sure the bad one got overwritte.
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
	})

	It("should be removed if the source no longer exists", func() {
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())

		removeRole(ctx, fooName, "foo-role")
		Eventually(HasObject(ctx, api.RoleResource, fooName, "foo-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeFalse())
	})

	It("should overwrite the propagated ones if the source is updated", func() {
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, fooName, "foo-role")).Should(BeTrue())
		Eventually(isModified(ctx, fooName, "foo-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Eventually(isModified(ctx, barName, "foo-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Eventually(isModified(ctx, bazName, "foo-role")).Should(BeFalse())

		modifyRole(ctx, fooName, "foo-role")
		Eventually(isModified(ctx, fooName, "foo-role")).Should(BeTrue())
		Eventually(isModified(ctx, barName, "foo-role")).Should(BeTrue())
		Eventually(isModified(ctx, bazName, "foo-role")).Should(BeTrue())
	})

	It("should overwrite the conflicting source in the descedants", func() {
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, barName, "bar-role")).Should(BeTrue())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "bar-role")).Should(Equal(barName))

		MakeObject(ctx, api.RoleResource, fooName, "bar-role")
		// Add a 500-millisecond gap here to allow updating the cached bar-roles in bar
		// and baz namespaces. Without this, even having 20 seconds in the "Eventually()"
		// funcs below, the test failed with timeout. Guess the reason is that it's
		// constantly getting the cached object.
		time.Sleep(500 * time.Millisecond)
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())
		Eventually(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "bar-role")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, barName, "bar-role")).Should(BeTrue())
		Eventually(ObjectInheritedFrom(ctx, api.RoleResource, barName, "bar-role")).Should(Equal(fooName))
	})

	It("should overwrite conflicting source with the top source that can propagate", func() {
		// Create a 'baz-role' in 'foo' that cannot propagate because of the finalizer.
		MakeObject(ctx, api.RoleResource, fooName, "baz-role")
		Eventually(HasObject(ctx, api.RoleResource, fooName, "baz-role")).Should(BeTrue())
		setFinalizer(ctx, fooName, "baz-role", true)
		// Create a 'baz-role' in 'bar' that can propagate.
		MakeObject(ctx, api.RoleResource, barName, "baz-role")

		// Before the tree is constructed, 'baz-role' shouldn't be overwritten.
		Eventually(HasObject(ctx, api.RoleResource, bazName, "baz-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "baz-role")).Should(Equal(""))

		// Construct the tree: foo (root) <- bar <- baz.
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, bazName, "baz-role")).Should(BeTrue())
		// The 'baz-role' in 'baz' should be overwritten by the conflicting one in
		// 'bar' but not 'foo', since the one in 'foo' cannot propagate with
		// finalizer. Add a 500-millisecond gap to allow overwriting the object.
		time.Sleep(500 * time.Millisecond)
		Eventually(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "baz-role")).Should(Equal(barName))
	})

	It("should have deletions propagated after crit conditions are removed", func() {
		// Create tree: bar -> foo (root) and make sure foo-role is propagated
		SetParent(ctx, barName, fooName)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())

		// Create a critical condition on foo (and also bar by extension)
		brumpfName := CreateNSName("brumpf")
		fooHier := NewOrGetHierarchy(ctx, fooName)
		fooHier.Spec.Parent = brumpfName
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(BeTrue())
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(BeTrue())

		// Delete the object from `foo`, wait until we're sure that it's gone, and then wait a while
		// longer and verify it *isn't* deleted from `bar`, because the critical condition has paused
		// deletions.
		DeleteObject(ctx, api.RoleResource, fooName, "foo-role")
		Eventually(HasObject(ctx, api.RoleResource, fooName, "foo-role")).Should(BeFalse())
		time.Sleep(1 * time.Second) // todo: merge with similar constants elsewhere
		Expect(HasObject(ctx, api.RoleResource, barName, "foo-role")()).Should(BeTrue())

		// Resolve the critical condition and verify that the object is deleted
		fooHier = NewOrGetHierarchy(ctx, fooName)
		fooHier.Spec.Parent = ""
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeFalse())
	})

	It("shouldn't propagate/delete if the namespace has Crit condition", func() {
		// Set tree as baz -> bar -> foo(root).
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)

		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role")).Should(Equal(fooName))

		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "foo-role")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "bar-role")).Should(Equal(barName))

		// Set foo's parent to a non-existent namespace.
		brumpfName := CreateNSName("brumpf")
		fooHier := NewOrGetHierarchy(ctx, fooName)
		fooHier.Spec.Parent = brumpfName
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(true))
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(true))
		Eventually(HasCondition(ctx, bazName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(true))

		// Set baz's parent to foo and add a new role in foo.
		SetParent(ctx, bazName, fooName)
		MakeObject(ctx, api.RoleResource, fooName, "foo-role-2")

		// Since the sync is frozen, baz should still have bar-role (no deleting).
		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "bar-role")).Should(Equal(barName))
		// baz and bar shouldn't have foo-role-2 (no propagating).
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role-2")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role-2")).Should(BeFalse())

		// Create the missing parent namespace with one object.
		brumpfNS := &corev1.Namespace{}
		brumpfNS.Name = brumpfName
		Expect(K8sClient.Create(ctx, brumpfNS)).Should(Succeed())
		MakeObject(ctx, api.RoleResource, brumpfName, "brumpf-role")

		// The Crit conditions should be gone.
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(false))
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(false))
		Eventually(HasCondition(ctx, bazName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(false))

		// Everything should be up to date after the Crit conditions are gone.
		Eventually(HasObject(ctx, api.RoleResource, fooName, "brumpf-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, fooName, "brumpf-role")).Should(Equal(brumpfName))

		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role-2")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role-2")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, barName, "brumpf-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "brumpf-role")).Should(Equal(brumpfName))

		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "foo-role")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role-2")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "foo-role-2")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, bazName, "brumpf-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "brumpf-role")).Should(Equal(brumpfName))

		Eventually(HasObject(ctx, api.RoleResource, bazName, "bar-role")).Should(BeFalse())
	})

	It("should generate CannotPropagate event if it's excluded from being propagated", func() {
		// Set tree as baz -> bar -> foo(root) and make sure the secret gets propagated.
		SetParent(ctx, barName, fooName)
		SetParent(ctx, bazName, barName)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "foo-role")).Should(Equal(fooName))

		// Verify there's no CannotPropagate event before introducing the error.
		Eventually(eventFor(ctx, fooName, "foo-role", api.EventCannotPropagate)).Should(Equal(""))

		// Make the secret unpropagateable and verify that it disappears.
		setFinalizer(ctx, fooName, "foo-role", true)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeFalse())
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeFalse())

		// Verify the CannotPropagate event from source object.
		Eventually(eventFor(ctx, fooName, "foo-role", api.EventCannotPropagate)).ShouldNot(Equal(""))

		// Fix the problem and verify that the role is propagated again. Please note
		// that events are removed one hour after the last occurrence. Therefore, we
		// should still see the CannotPropagate event after fixing the issue.
		setFinalizer(ctx, fooName, "foo-role", false)
		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role")).Should(Equal(fooName))
		Eventually(HasObject(ctx, api.RoleResource, bazName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, bazName, "foo-role")).Should(Equal(fooName))
		Eventually(eventFor(ctx, fooName, "foo-role", api.EventCannotPropagate)).ShouldNot(Equal(""))
	})

	It("shouldn't delete a descendant source object with the same name if the sync mode is 'Remove'", func() {
		AddToHNCConfig(ctx, "", "secrets", api.Remove)
		// Set tree as bar -> foo(root).
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "secrets", barName, "bar-sec")
		Eventually(HasObject(ctx, "secrets", barName, "bar-sec")).Should(BeTrue())

		// Create an object with the same name in the parent.
		MakeObject(ctx, "secrets", fooName, "bar-sec")
		Eventually(HasObject(ctx, "secrets", fooName, "bar-sec")).Should(BeTrue())
		// Give the reconciler some time to remove the object if it's going to.
		time.Sleep(500 * time.Millisecond)
		// The source object in the child shouldn't be deleted since the type has 'Remove' mode.
		Eventually(HasObject(ctx, "secrets", barName, "bar-sec")).Should(BeTrue())
	})

	It("should avoid propagating banned annotations", func() {
		SetParent(ctx, barName, fooName)
		MakeObjectWithAnnotations(ctx, "roles", fooName, "foo-annot-role", map[string]string{
			"annot-a": "value-a",
			"annot-b": "value-b",
		})

		// Ensure the object is propagated with both annotations
		Eventually(func() error {
			inst, err := GetObject(ctx, "roles", barName, "foo-annot-role")
			if err != nil {
				return err
			}
			annots := inst.GetAnnotations()
			if annots["annot-a"] != "value-a" {
				return fmt.Errorf("annot-a: want 'value-a', got %q", annots["annot-a"])
			}
			if annots["annot-b"] != "value-b" {
				return fmt.Errorf("annot-b: want 'value-b', got %q", annots["annot-b"])
			}
			return nil
		}).Should(Succeed(), "waiting for initial sync of foo-annot-role")
		DeleteObject(ctx, "roles", fooName, "foo-annot-role")

		// Tell the HNC config not to propagate annot-a and verify that this time, it's not annotated
		config.UnpropagatedAnnotations = []string{"annot-a"}
		MakeObjectWithAnnotations(ctx, "roles", fooName, "foo-annot-role", map[string]string{
			"annot-a": "value-a",
			"annot-b": "value-b",
		})

		// Verify that the annotation no longer appears
		Eventually(func() error {
			inst, err := GetObject(ctx, "roles", barName, "foo-annot-role")
			if err != nil {
				return err
			}
			annots := inst.GetAnnotations()
			if val, ok := annots["annot-a"]; ok {
				return fmt.Errorf("annot-a: wanted it to be missing, got %q", val)
			}
			if annots["annot-b"] != "value-b" {
				return fmt.Errorf("annot-b: want 'value-b', got %q", annots["annot-b"])
			}
			return nil
		}).Should(Succeed(), "waiting for annot-a to be unpropagated")
	})

	It("should avoid propagating when no selector is set if the sync mode is 'AllowPropagate'", func() {
		AddToHNCConfig(ctx, "", "secrets", api.AllowPropagate)
		// Set tree as bar -> foo(root).
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "secrets", fooName, "foo-sec")
		Eventually(HasObject(ctx, "secrets", fooName, "foo-sec")).Should(BeTrue())

		// Ensure the object is not propagated
		Consistently(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeFalse())

		// Update the secret object with the treeSelector annotation
		UpdateObjectWithAnnotations(ctx, "secrets", fooName, "foo-sec", map[string]string{
			api.AnnotationTreeSelector: barName,
		})

		// Ensure the object is now propagated
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "secrets", barName, "foo-sec")).Should(Equal(fooName))

		// Remove the annotation from the secret and ensure the object is deleted from the descendant
		UpdateObjectWithAnnotations(ctx, "secrets", fooName, "foo-sec", map[string]string{})
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeFalse())
	})
})

func modifyRole(ctx context.Context, nsName, roleName string) {
	nnm := types.NamespacedName{Namespace: nsName, Name: roleName}
	role := &v1.Role{}
	ExpectWithOffset(1, K8sClient.Get(ctx, nnm, role)).Should(Succeed())

	labels := role.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["modify"] = "make-a-change"
	role.SetLabels(labels)
	ExpectWithOffset(1, K8sClient.Update(ctx, role)).Should(Succeed())
}

func setFinalizer(ctx context.Context, nsName, roleName string, set bool) {
	nnm := types.NamespacedName{Namespace: nsName, Name: roleName}
	role := &v1.Role{}
	ExpectWithOffset(1, K8sClient.Get(ctx, nnm, role)).Should(Succeed())
	if set {
		role.ObjectMeta.Finalizers = []string{"example.com/foo"}
	} else {
		role.ObjectMeta.Finalizers = nil
	}
	ExpectWithOffset(1, K8sClient.Update(ctx, role)).Should(Succeed())
}

func isModified(ctx context.Context, nsName, roleName string) func() bool {
	// `Eventually` only works with a fn that doesn't take any args.
	return func() bool {
		nnm := types.NamespacedName{Namespace: nsName, Name: roleName}
		role := &v1.Role{}
		// Even if `isModified` is always called after `HasObject`, we still use `Eventually`
		// here to make sure there's no weird case of failure when the object does exist. This
		// will not increase the test time since it will pass immediately if it succeeds.
		EventuallyWithOffset(1, func() error {
			return K8sClient.Get(ctx, nnm, role)
		}).Should(Succeed())

		labels := role.GetLabels()
		_, ok := labels["modify"]
		return ok
	}
}

func removeRole(ctx context.Context, nsName, roleName string) {
	role := &v1.Role{}
	role.Name = roleName
	role.Namespace = nsName
	ExpectWithOffset(1, K8sClient.Delete(ctx, role)).Should(Succeed())
}

func eventFor(ctx context.Context, nsName, objName, reason string) func() string {
	return func() string {
		eventList := &corev1.EventList{}
		EventuallyWithOffset(1, func() error {
			return K8sClient.List(ctx, eventList, &client.ListOptions{Namespace: nsName})
		}).Should(Succeed())

		for _, event := range eventList.Items {
			if event.InvolvedObject.Name == objName && event.Reason == reason {
				return fmt.Sprintf("%+v", event)
			}
		}
		return ""
	}
}
