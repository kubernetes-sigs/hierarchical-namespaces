package hierarchyconfig_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	. "sigs.k8s.io/hierarchical-namespaces/internal/integtest"
)

func TestInteg(t *testing.T) {
	HNCRun(t, "Hierarchy Configuration reconciler")
}

var _ = BeforeSuite(HNCBeforeSuite, 60)
var _ = AfterSuite(HNCAfterSuite)

var _ = Describe("Hierarchy", func() {
	ctx := context.Background()

	var (
		fooName string
		barName string
	)

	BeforeEach(func() {
		fooName = CreateNS(ctx, "foo")
		barName = CreateNS(ctx, "bar")
		config.SetNamespaces("")
		config.SetManagedMeta(nil, nil)
	})

	It("should set a child on the parent", func() {
		fooHier := NewHierarchy(fooName)
		fooHier.Spec.Parent = barName
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasChild(ctx, barName, fooName)).Should(Equal(true))
	})

	It("should set IllegalParent condition if the parent is an excluded namespace", func() {
		// Set bar's parent to the excluded-namespace "kube-system".
		config.SetNamespaces("", "kube-system")
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = "kube-system"
		UpdateHierarchy(ctx, barHier)
		// Bar singleton should have "IllegalParent" and no "ParentMissing" condition.
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonIllegalParent)).Should(Equal(true))
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(false))
	})

	It("should set ParentMissing condition if the parent is missing", func() {
		// Set up the parent-child relationship
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = "brumpf"
		UpdateHierarchy(ctx, barHier)
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(true))
	})

	It("should unset ParentMissing condition if the parent is later created", func() {
		// Set up the parent-child relationship with the missing name
		brumpfName := CreateNSName("brumpf")
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = brumpfName
		UpdateHierarchy(ctx, barHier)
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(true))

		// Create the missing parent
		brumpfNS := &corev1.Namespace{}
		brumpfNS.Name = brumpfName
		Expect(K8sClient.Create(ctx, brumpfNS)).Should(Succeed())

		// Ensure the condition is resolved on the child
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(false))

		// Ensure the child is listed on the parent
		Eventually(HasChild(ctx, brumpfName, barName)).Should(Equal(true))
	})

	It("should set AncestorHaltActivities condition if any ancestor has critical condition", func() {
		// Set up the parent-child relationship
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = "brumpf"
		UpdateHierarchy(ctx, barHier)
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(true))

		// Set bar as foo's parent
		fooHier := NewHierarchy(fooName)
		fooHier.Spec.Parent = barName
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(true))
	})

	It("should unset AncestorHaltActivities condition if critical conditions in ancestors are gone", func() {
		// Set up the parent-child relationship with the missing name
		brumpfName := CreateNSName("brumpf")
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = brumpfName
		UpdateHierarchy(ctx, barHier)
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(true))

		// Set bar as foo's parent
		fooHier := NewHierarchy(fooName)
		fooHier.Spec.Parent = barName
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(true))

		// Create the missing parent
		brumpfNS := &corev1.Namespace{}
		brumpfNS.Name = brumpfName
		Expect(K8sClient.Create(ctx, brumpfNS)).Should(Succeed())

		// Ensure the condition is resolved on the child
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonParentMissing)).Should(Equal(false))

		// Ensure the child is listed on the parent
		Eventually(HasChild(ctx, brumpfName, barName)).Should(Equal(true))

		// Ensure foo is enqueued and thus get AncestorHaltActivities condition updated after
		// critical conditions are resolved in bar.
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonAncestor)).Should(Equal(false))
	})

	It("should set InCycle condition if a self-cycle is detected", func() {
		fooHier := NewHierarchy(fooName)
		fooHier.Spec.Parent = fooName
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonInCycle)).Should(Equal(true))
	})

	It("should set InCycle condition if a cycle is detected", func() {
		// Set up initial hierarchy
		SetParent(ctx, barName, fooName)
		Eventually(HasChild(ctx, fooName, barName)).Should(Equal(true))

		// Break it
		SetParent(ctx, fooName, barName)
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonInCycle)).Should(Equal(true))
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonInCycle)).Should(Equal(true))

		// Fix it
		SetParent(ctx, fooName, "")
		Eventually(HasCondition(ctx, fooName, api.ConditionActivitiesHalted, api.ReasonInCycle)).Should(Equal(false))
		Eventually(HasCondition(ctx, barName, api.ConditionActivitiesHalted, api.ReasonInCycle)).Should(Equal(false))
	})

	It("should have a tree label", func() {
		// Make bar a child of foo
		SetParent(ctx, barName, fooName)

		// Verify all the labels
		Eventually(GetLabel(ctx, barName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, barName, barName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, fooName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
	})

	It("should propagate external tree labels", func() {
		// Create an external namespace baz
		l := map[string]string{
			"ext2" + api.LabelTreeDepthSuffix: "2",
			"ext1" + api.LabelTreeDepthSuffix: "1",
		}
		a := map[string]string{api.AnnotationManagedBy: "others"}
		bazName := CreateNSWithLabelAnnotation(ctx, "baz", l, a)

		// Make bar a child of baz
		SetParent(ctx, barName, bazName)

		// Verify all the labels
		Eventually(GetLabel(ctx, barName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal("3"))
		Eventually(GetLabel(ctx, barName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		// Even if baz doesn't have the tree label of itself with depth 0 when created,
		// the tree label of itself is added automatically.
		Eventually(GetLabel(ctx, barName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, barName, barName+api.LabelTreeDepthSuffix)).Should(Equal("0"))

		Eventually(GetLabel(ctx, bazName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, bazName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
	})

	It("should propagate external tree labels if the internal namespace is converted to external", func() {
		// Make bar a child of foo
		SetParent(ctx, barName, fooName)

		// Verify all the labels
		Eventually(GetLabel(ctx, barName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, barName, barName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, fooName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("0"))

		// Convert foo from an internal namespace to an external namespace by adding
		// the "managed-by: others" annotation.
		ns := GetNamespace(ctx, fooName)
		ns.SetAnnotations(map[string]string{api.AnnotationManagedBy: "others"})
		l := map[string]string{
			"ext2" + api.LabelTreeDepthSuffix: "2",
			"ext1" + api.LabelTreeDepthSuffix: "1",
		}
		ns.SetLabels(l)
		UpdateNamespace(ctx, ns)

		// Verify all the labels
		Eventually(GetLabel(ctx, barName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal("3"))
		Eventually(GetLabel(ctx, barName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		// Even if foo doesn't have the tree label of itself with depth 0 when created,
		// the tree label of itself is added automatically.
		Eventually(GetLabel(ctx, barName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, barName, barName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, fooName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, fooName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, fooName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
	})

	It("should remove external tree labels if the external namespace is converted to internal", func() {
		// Create an external namespace baz
		l := map[string]string{
			"ext2" + api.LabelTreeDepthSuffix: "2",
			"ext1" + api.LabelTreeDepthSuffix: "1",
		}
		a := map[string]string{api.AnnotationManagedBy: "others"}
		bazName := CreateNSWithLabelAnnotation(ctx, "baz", l, a)

		// Make bar a child of baz
		SetParent(ctx, barName, bazName)

		// Verify all the labels
		Eventually(GetLabel(ctx, barName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal("3"))
		Eventually(GetLabel(ctx, barName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		// Even if baz doesn't have the tree label of itself with depth 0 when created,
		// the tree label of itself is added automatically.
		Eventually(GetLabel(ctx, barName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, barName, barName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, bazName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))

		// Convert baz from an external namespace to an internal namespace by removing
		// the "managed-by: others" annotation.
		ns := GetNamespace(ctx, bazName)
		ns.SetAnnotations(map[string]string{})
		UpdateNamespace(ctx, ns)

		// Verify all the labels
		Eventually(GetLabel(ctx, barName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, barName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, barName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, barName, barName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, "ext2"+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, bazName, "ext1"+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
	})

	It("should update labels when parent is changed", func() {
		// Set up key-value pair for non-HNC label
		const keyName = "key"
		const valueName = "value"

		// Set up initial hierarchy
		bazName := CreateNSWithLabel(ctx, "baz", map[string]string{keyName: valueName})
		bazHier := NewHierarchy(bazName)
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, keyName)).Should(Equal(valueName))

		// Make baz as a child of bar
		bazHier.Spec.Parent = barName
		UpdateHierarchy(ctx, bazHier)

		// Verify all labels on baz after set bar as parent
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, barName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, bazName, keyName)).Should(Equal(valueName))

		// Change parent to foo
		bazHier.Spec.Parent = fooName
		UpdateHierarchy(ctx, bazHier)

		// Verify all labels on baz after change parent to foo
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, fooName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, bazName, barName+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, bazName, keyName)).Should(Equal(valueName))
	})

	It("should update labels when parent is removed", func() {
		// Set up key-value pair for non-HNC label
		const keyName = "key"
		const valueName = "value"

		// Set up initial hierarchy
		bazName := CreateNSWithLabel(ctx, "baz", map[string]string{keyName: valueName})
		bazHier := NewHierarchy(bazName)
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, keyName)).Should(Equal(valueName))

		// Make baz as a child of bar
		bazHier.Spec.Parent = barName
		UpdateHierarchy(ctx, bazHier)

		// Verify all labels on baz after set bar as parent
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, barName+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, bazName, keyName)).Should(Equal(valueName))

		// Remove parent from baz
		bazHier.Spec.Parent = ""
		UpdateHierarchy(ctx, bazHier)

		// Verify all labels on baz after parent removed
		Eventually(GetLabel(ctx, bazName, bazName+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, bazName, barName+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, bazName, keyName)).Should(Equal(valueName))
	})

	It("should clear tree labels that are involved in a cycle, except the first one", func() {
		// Create the initial tree:
		// a(0) -+- b(1) -+- d(3) --- f(5)
		//       +- c(2)  +- e(4)
		nms := CreateNSes(ctx, 6)
		SetParent(ctx, nms[1], nms[0])
		SetParent(ctx, nms[2], nms[0])
		SetParent(ctx, nms[3], nms[1])
		SetParent(ctx, nms[4], nms[1])
		SetParent(ctx, nms[5], nms[3])

		// Check all labels
		Eventually(GetLabel(ctx, nms[0], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[1], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[1], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[2], nms[2]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[2], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[3], nms[3]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[3], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[3], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, nms[4], nms[4]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[4], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[4], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, nms[5], nms[5]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[5], nms[3]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[5], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, nms[5], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("3"))

		// Create a cycle from a(0) to d(3) and check all labels.
		SetParent(ctx, nms[0], nms[3])
		Eventually(GetLabel(ctx, nms[0], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[1], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[1], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, nms[2], nms[2]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[2], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[3], nms[3]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[3], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, nms[3], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, nms[4], nms[4]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[4], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[4], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, nms[5], nms[5]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[5], nms[3]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[5], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal(""))
		Eventually(GetLabel(ctx, nms[5], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal(""))

		// Fix the cycle and ensure that everything's restored
		SetParent(ctx, nms[0], "")
		Eventually(GetLabel(ctx, nms[0], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[1], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[1], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[2], nms[2]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[2], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[3], nms[3]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[3], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[3], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, nms[4], nms[4]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[4], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[4], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, nms[5], nms[5]+api.LabelTreeDepthSuffix)).Should(Equal("0"))
		Eventually(GetLabel(ctx, nms[5], nms[3]+api.LabelTreeDepthSuffix)).Should(Equal("1"))
		Eventually(GetLabel(ctx, nms[5], nms[1]+api.LabelTreeDepthSuffix)).Should(Equal("2"))
		Eventually(GetLabel(ctx, nms[5], nms[0]+api.LabelTreeDepthSuffix)).Should(Equal("3"))
	})

	It("should remove included-namespace namespace labels from excluded namespaces", func() {
		config.SetNamespaces("", "kube-system")
		kubeSystem := GetNamespace(ctx, "kube-system")

		// Add additional label "other:other" to verify the labels are updated.
		l := map[string]string{api.LabelIncludedNamespace: "true", "other": "other"}
		kubeSystem.SetLabels(l)
		UpdateNamespace(ctx, kubeSystem)
		// Verify the labels are updated on the namespace.
		Eventually(GetLabel(ctx, "kube-system", "other")).Should(Equal("other"))
		// Verify the included-namespace label is removed by the HC reconciler.
		Eventually(GetLabel(ctx, "kube-system", api.LabelIncludedNamespace)).Should(Equal(""))
	})

	It("should set included-namespace namespace labels properly on non-excluded namespaces", func() {
		// Create a namespace without any labels.
		fooName := CreateNS(ctx, "foo")
		// Verify the label is eventually added by the HC reconciler.
		Eventually(GetLabel(ctx, fooName, api.LabelIncludedNamespace)).Should(Equal("true"))

		l := map[string]string{api.LabelIncludedNamespace: "false"}
		// Create a namespace with the label with a wrong value.
		barName := CreateNSWithLabel(ctx, "bar", l)
		// Verify the label is eventually updated to have the right value.
		Eventually(GetLabel(ctx, barName, api.LabelIncludedNamespace)).Should(Equal("true"))
	})

	It("should propagate managed labels and not unmanaged labels", func() {
		config.SetManagedMeta([]string{"legal-.*"}, nil)

		// Set the managed label and confirm that it only exists on one namespace
		fooHier := NewHierarchy(fooName)
		fooHier.Spec.Labels = []api.MetaKVP{
			{Key: "legal-label", Value: "val-1"},
			{Key: "unpropagated-label", Value: "not-allowed"},
		}
		UpdateHierarchy(ctx, fooHier)
		Eventually(GetLabel(ctx, fooName, "legal-label")).Should(Equal("val-1"))
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal(""))

		// Add 'bar' as a child and verify the right labels are propagated
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = fooName
		UpdateHierarchy(ctx, barHier)
		Eventually(HasChild(ctx, fooName, barName)).Should(Equal(true))
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal("val-1"))

		// Verify that the bad label isn't propagated and that a condition is set
		Eventually(GetLabel(ctx, fooName, "unpropagated-label")).Should(Equal(""))
		Eventually(GetLabel(ctx, barName, "unpropagated-label")).Should(Equal(""))
		Eventually(HasCondition(ctx, fooName, api.ConditionBadConfiguration, api.ReasonIllegalManagedLabel)).Should(Equal(true))

		// Remove the bad config and verify that the condition is removed
		fooHier = GetHierarchy(ctx, fooName)
		fooHier.Spec.Labels = fooHier.Spec.Labels[0:1]
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionBadConfiguration, api.ReasonIllegalManagedLabel)).Should(Equal(false))

		// Change the value of the label and verify that it's propagated
		fooHier = GetHierarchy(ctx, fooName)
		fooHier.Spec.Labels[0].Value = "second-value"
		UpdateHierarchy(ctx, fooHier)
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal("second-value"))

		// Remove 'bar' as a child and verify that the label is removed
		barHier = GetHierarchy(ctx, barName)
		barHier.Spec.Parent = ""
		UpdateHierarchy(ctx, barHier)
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal(""))

		// TODO: test external namespace, multiple regexes, etc
	})

	It("should propagate managed annotations and not unmanaged annotations", func() {
		config.SetManagedMeta(nil, []string{"legal-.*"})

		// Set the managed annotation and confirm that it only exists on one namespace
		fooHier := NewHierarchy(fooName)
		fooHier.Spec.Annotations = []api.MetaKVP{
			{Key: "legal-annotation", Value: "val-1"},
			{Key: "unpropagated-annotation", Value: "not-allowed"},
		}
		UpdateHierarchy(ctx, fooHier)
		Eventually(GetAnnotation(ctx, fooName, "legal-annotation")).Should(Equal("val-1"))
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal(""))

		// Add 'bar' as a child and verify the right annotations are propagated
		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = fooName
		UpdateHierarchy(ctx, barHier)
		Eventually(HasChild(ctx, fooName, barName)).Should(Equal(true))
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal("val-1"))

		// Verify that the bad annotation isn't propagated and that a condition is set
		Eventually(GetAnnotation(ctx, fooName, "unpropagated-annotation")).Should(Equal(""))
		Eventually(GetAnnotation(ctx, barName, "unpropagated-annotation")).Should(Equal(""))
		Eventually(HasCondition(ctx, fooName, api.ConditionBadConfiguration, api.ReasonIllegalManagedAnnotation)).Should(Equal(true))

		// Remove the bad config and verify that the condition is removed
		fooHier = GetHierarchy(ctx, fooName)
		fooHier.Spec.Annotations = fooHier.Spec.Annotations[0:1]
		UpdateHierarchy(ctx, fooHier)
		Eventually(HasCondition(ctx, fooName, api.ConditionBadConfiguration, api.ReasonIllegalManagedAnnotation)).Should(Equal(false))

		// Change the value of the annotation and verify that it's propagated
		fooHier = GetHierarchy(ctx, fooName)
		fooHier.Spec.Annotations[0].Value = "second-value"
		UpdateHierarchy(ctx, fooHier)
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal("second-value"))

		// Remove 'bar' as a child and verify that the annotation is removed
		barHier = GetHierarchy(ctx, barName)
		barHier.Spec.Parent = ""
		UpdateHierarchy(ctx, barHier)
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal(""))

		// TODO: test external namespaces, multiple regexes, etc
	})
})
