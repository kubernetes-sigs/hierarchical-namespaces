package anchor_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	. "sigs.k8s.io/hierarchical-namespaces/internal/integtest"
)

func TestInteg(t *testing.T) {
	HNCRun(t, "Anchor reconciler")
}

var _ = BeforeSuite(HNCBeforeSuite)
var _ = AfterSuite(HNCAfterSuite)

var _ = Describe("Anchor", func() {
	ctx := context.Background()

	var (
		fooName string
		barName string
		bazName string
	)

	BeforeEach(func() {
		fooName = CreateNS(ctx, "foo")
		barName = CreateNSName("bar")
		bazName = CreateNSName("baz")
		config.SetNamespaces("")
	})

	It("should create an subnamespace and update the hierarchy according to the anchor", func() {
		// Create 'bar' anchor in 'foo' namespace.
		foo_anchor_bar := newAnchor(barName, fooName)
		updateAnchor(ctx, foo_anchor_bar)

		// It should create the namespace 'bar' with 'foo' in the subnamespace-of annotation.
		Eventually(func() string {
			return GetNamespace(ctx, barName).GetAnnotations()[api.SubnamespaceOf]
		}).Should(Equal(fooName))

		// It should set the subnamespace "bar" as a child of the parent "foo".
		Eventually(func() []string {
			fooHier := GetHierarchy(ctx, fooName)
			return fooHier.Status.Children
		}).Should(Equal([]string{barName}))

		// It should set the parent namespace "foo" as the parent of "bar".
		Eventually(func() string {
			barHier := GetHierarchy(ctx, barName)
			return barHier.Spec.Parent
		}).Should(Equal(fooName))

		// It should set the anchor.status.state to Ok if the above sub-tests all pass.
		Eventually(getAnchorState(ctx, fooName, barName)).Should(Equal(api.Ok))
	})

	It("should remove the anchor in an excluded namespace", func() {
		config.SetNamespaces("", "kube-system")
		kube_system_anchor_bar := newAnchor(barName, "kube-system")
		updateAnchor(ctx, kube_system_anchor_bar)
		Eventually(canGetAnchor(ctx, barName, "kube-system")).Should(Equal(false))
	})

	It("should set the anchor.status.state to Forbidden if the subnamespace is an excluded namespace", func() {
		config.SetNamespaces("", "kube-system")
		foo_anchor_kube_system := newAnchor("kube-system", fooName)
		updateAnchor(ctx, foo_anchor_kube_system)
		Eventually(getAnchorState(ctx, fooName, "kube-system")).Should(Equal(api.Forbidden))
	})

	It("should set the anchor.status.state to Conflict if a namespace of the same name already exists", func() {
		// Create "baz" namespace.
		bazName := CreateNS(ctx, "baz")
		// Create an anchor still with the same name "baz" in "foo" namespace.
		foo_anchor_baz := newAnchor(bazName, fooName)
		updateAnchor(ctx, foo_anchor_baz)
		Eventually(getAnchorState(ctx, fooName, bazName)).Should(Equal(api.Conflict))
	})

	It("should always set the owner as the parent if otherwise", func() {
		// Create "bar" anchor in "foo" namespace.
		foo_anchor_bar := newAnchor(barName, fooName)
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(func() string {
			barHier := GetHierarchy(ctx, barName)
			return barHier.Spec.Parent
		}).Should(Equal(fooName))

		// Change the bar's parent. Additionally change another field to reflect the
		// update of the HC instance (hc.Spec.AllowCascadingDeletion).
		Eventually(func() error {
			barHier := GetHierarchy(ctx, barName)
			barHier.Spec.Parent = "other"
			barHier.Spec.AllowCascadingDeletion = true
			return TryUpdateHierarchy(ctx, barHier) // can fail if made too quickly
		}).Should(Succeed())

		// The parent of 'bar' should be set back to 'foo' after reconciliation.
		Eventually(func() bool {
			barHier := GetHierarchy(ctx, barName)
			return barHier.Spec.AllowCascadingDeletion
		}).Should(Equal(true))
		Eventually(func() string {
			barHier := GetHierarchy(ctx, barName)
			return barHier.Spec.Parent
		}).Should(Equal(fooName))
	})

	It("should propagate managed labels and not unmanaged labels", func() {
		Expect(config.SetManagedMeta([]string{"legal-.*"}, nil)).Should(Succeed())

		// Set the managed label and confirm that it only exists on one namespace
		foo_anchor_bar := newAnchor(barName, fooName)
		foo_anchor_bar.Spec.Labels = []api.MetaKVP{
			{Key: "legal-label", Value: "val-1"},
			{Key: "unpropagated-label", Value: "not-allowed"},
		}
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal("val-1"))

		// Add 'baz' as a child and verify the right labels are propagated
		bar_anchor_baz := newAnchor(bazName, barName)
		updateAnchor(ctx, bar_anchor_baz)
		Eventually(HasChild(ctx, barName, bazName)).Should(Equal(true))
		Eventually(GetLabel(ctx, bazName, "legal-label")).Should(Equal("val-1"))

		// Verify that the bad label isn't propagated and that a condition is set
		Eventually(GetLabel(ctx, barName, "unpropagated-label")).Should(Equal(""))
		Eventually(GetLabel(ctx, bazName, "unpropagated-label")).Should(Equal(""))
		Eventually(HasCondition(ctx, barName, api.ConditionBadConfiguration, api.ReasonIllegalManagedLabel)).Should(Equal(true))

		// Remove the bad config and verify that the condition is removed
		foo_anchor_bar = getAnchor(ctx, fooName, barName)
		foo_anchor_bar.Spec.Labels = foo_anchor_bar.Spec.Labels[0:1]
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(HasCondition(ctx, barName, api.ConditionBadConfiguration, api.ReasonIllegalManagedLabel)).Should(Equal(false))

		// Change the value of the label and verify that it's propagated
		foo_anchor_bar = getAnchor(ctx, fooName, barName)
		foo_anchor_bar.Spec.Labels[0].Value = "second-value"
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal("second-value"))

		//Remove label from hierarchyconfiguration and verify that the label is NOT removed
		barHier := GetHierarchy(ctx, barName)
		barHier.Spec.Labels = []api.MetaKVP{}
		UpdateHierarchy(ctx, barHier)
		Consistently(GetLabel(ctx, barName, "legal-label")).Should(Equal("second-value"))

		//Remove subnamespace-of annotation from child namespace and verify anchor is in conflict
		barNs := GetNamespace(ctx, barName)
		delete(barNs.GetAnnotations(), api.SubnamespaceOf)
		UpdateNamespace(ctx, barNs)
		Eventually(getAnchorState(ctx, fooName, barName)).Should(Equal(api.Conflict))

		//Delete parent anchor with labels and verify that label is not removed
		DeleteObject(ctx, "subnamespaceanchors", fooName, barName)
		Consistently(GetLabel(ctx, barName, "legal-label")).Should(Equal("second-value"))

		//Remove label from hierarchyconfiguration and verify that label is removed
		barHier = GetHierarchy(ctx, barName)
		barHier.Spec.Labels = []api.MetaKVP{}
		UpdateHierarchy(ctx, barHier)
		Eventually(GetLabel(ctx, barName, "legal-label")).Should(Equal(""))
	})

	It("should propagate managed annotations and not unmanaged annotations", func() {
		Expect(config.SetManagedMeta(nil, []string{"legal-.*"})).Should(Succeed())

		// Set the managed annotation and confirm that it only exists on one namespace
		foo_anchor_bar := newAnchor(barName, fooName)
		foo_anchor_bar.Spec.Annotations = []api.MetaKVP{
			{Key: "legal-annotation", Value: "val-1"},
			{Key: "unpropagated-annotation", Value: "not-allowed"},
		}
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal("val-1"))

		// Add 'baz' as a child and verify the right annotations are propagated
		bar_anchor_baz := newAnchor(bazName, barName)
		updateAnchor(ctx, bar_anchor_baz)
		Eventually(HasChild(ctx, barName, bazName)).Should(Equal(true))
		Eventually(GetAnnotation(ctx, bazName, "legal-annotation")).Should(Equal("val-1"))

		// Verify that the bad annotation isn't propagated and that a condition is set
		Eventually(GetAnnotation(ctx, barName, "unpropagated-annotation")).Should(Equal(""))
		Eventually(GetAnnotation(ctx, bazName, "unpropagated-annotation")).Should(Equal(""))
		Eventually(HasCondition(ctx, barName, api.ConditionBadConfiguration, api.ReasonIllegalManagedAnnotation)).Should(Equal(true))

		// Remove the bad config and verify that the condition is removed
		foo_anchor_bar = getAnchor(ctx, fooName, barName)
		foo_anchor_bar.Spec.Annotations = foo_anchor_bar.Spec.Annotations[0:1]
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(HasCondition(ctx, barName, api.ConditionBadConfiguration, api.ReasonIllegalManagedAnnotation)).Should(Equal(false))

		// Change the value of the annotation and verify that it's propagated
		foo_anchor_bar = getAnchor(ctx, fooName, barName)
		foo_anchor_bar.Spec.Annotations[0].Value = "second-value"
		updateAnchor(ctx, foo_anchor_bar)
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal("second-value"))

		//Remove annotation from hierarchyconfiguration and verify that the annotation is NOT removed
		barHier := GetHierarchy(ctx, barName)
		barHier.Spec.Annotations = []api.MetaKVP{}
		UpdateHierarchy(ctx, barHier)
		Consistently(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal("second-value"))

		//Remove subnamespace-of annotation from child namespace and verify anchor is in conflict
		barNs := GetNamespace(ctx, barName)
		delete(barNs.GetAnnotations(), api.SubnamespaceOf)
		UpdateNamespace(ctx, barNs)
		Eventually(getAnchorState(ctx, fooName, barName)).Should(Equal(api.Conflict))

		//Delete parent anchor with annotations and verify that annotation is not removed
		DeleteObject(ctx, "subnamespaceanchors", fooName, barName)
		Consistently(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal("second-value"))

		//Remove label from hierarchyconfiguration and verify that annotation is removed
		barHier = GetHierarchy(ctx, barName)
		barHier.Spec.Annotations = []api.MetaKVP{}
		UpdateHierarchy(ctx, barHier)
		Eventually(GetAnnotation(ctx, barName, "legal-annotation")).Should(Equal(""))
	})
})

func getAnchorState(ctx context.Context, pnm, nm string) func() api.SubnamespaceAnchorState {
	return func() api.SubnamespaceAnchorState {
		return getAnchor(ctx, pnm, nm).Status.State
	}
}

func newAnchor(anm, nm string) *api.SubnamespaceAnchor {
	anchor := &api.SubnamespaceAnchor{}
	anchor.ObjectMeta.Namespace = nm
	anchor.ObjectMeta.Name = anm
	return anchor
}

func getAnchor(ctx context.Context, pnm, nm string) *api.SubnamespaceAnchor {
	return getAnchorWithOffset(1, ctx, pnm, nm)
}

func getAnchorWithOffset(offset int, ctx context.Context, pnm, nm string) *api.SubnamespaceAnchor {
	nsn := types.NamespacedName{Name: nm, Namespace: pnm}
	anchor := &api.SubnamespaceAnchor{}
	EventuallyWithOffset(offset+1, func() error {
		return K8sClient.Get(ctx, nsn, anchor)
	}).Should(Succeed())
	return anchor
}

func canGetAnchor(ctx context.Context, pnm, nm string) func() bool {
	return func() bool {
		nsn := types.NamespacedName{Name: nm, Namespace: pnm}
		anchor := &api.SubnamespaceAnchor{}
		if err := K8sClient.Get(ctx, nsn, anchor); err != nil {
			return false
		}
		return true
	}
}

func updateAnchor(ctx context.Context, anchor *api.SubnamespaceAnchor) {
	if anchor.CreationTimestamp.IsZero() {
		ExpectWithOffset(1, K8sClient.Create(ctx, anchor)).Should(Succeed())
	} else {
		ExpectWithOffset(1, K8sClient.Update(ctx, anchor)).Should(Succeed())
	}
}
