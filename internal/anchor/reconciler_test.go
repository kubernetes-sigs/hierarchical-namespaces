package anchor_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	. "sigs.k8s.io/hierarchical-namespaces/internal/integtest"
)

func TestInteg(t *testing.T) {
	HNCRun(t, "Anchor reconciler")
}

var _ = BeforeSuite(HNCBeforeSuite, 60)
var _ = AfterSuite(HNCAfterSuite)

var _ = Describe("Anchor", func() {
	ctx := context.Background()

	var (
		fooName string
		barName string
	)

	BeforeEach(func() {
		fooName = CreateNS(ctx, "foo")
		barName = CreateNSName("bar")
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
