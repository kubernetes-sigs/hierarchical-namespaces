package hrq_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/hrq"
	"sigs.k8s.io/hierarchical-namespaces/internal/hrq/utils"
	. "sigs.k8s.io/hierarchical-namespaces/internal/integtest"
	"sigs.k8s.io/hierarchical-namespaces/internal/metadata"
)

// createdHRQs keeps track of HierarchicalResourceQuota objects created in a
// test case so that we can clean them up properly after the test case.
var createdHRQs = []*api.HierarchicalResourceQuota{}

var _ = Describe("RQ reconciler tests", func() {
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

		barHier := NewHierarchy(barName)
		barHier.Spec.Parent = fooName
		UpdateHierarchy(ctx, barHier)

		bazHier := NewHierarchy(bazName)
		bazHier.Spec.Parent = fooName
		UpdateHierarchy(ctx, bazHier)

		Eventually(HasChild(ctx, fooName, barName)).Should(Equal(true))
		Eventually(HasChild(ctx, fooName, bazName)).Should(Equal(true))
	})

	AfterEach(func() {
		cleanupHRQObjects(ctx)
	})

	It("should set hard limits for each namespace", func() {
		setHRQ(ctx, fooHRQName, fooName, "secrets", "6", "pods", "3")
		setHRQ(ctx, barHRQName, barName, "secrets", "100", "cpu", "50")
		setHRQ(ctx, bazHRQName, bazName, "pods", "1")

		Eventually(getRQSpec(ctx, fooName)).Should(equalRL("secrets", "6", "pods", "3"))
		// Even though bar allows 100 secrets, the secret limit in the ResourceQuota object in bar
		// should be 6, which is the strictest limit among bar and its ancestor (i.e., foo).
		Eventually(getRQSpec(ctx, barName)).Should(equalRL("secrets", "6", "pods", "3", "cpu", "50"))
		Eventually(getRQSpec(ctx, bazName)).Should(equalRL("secrets", "6", "pods", "1"))
	})

	It("should update in-memory resource usages after ResourceQuota status changes", func() {
		setHRQ(ctx, fooHRQName, fooName, "secrets", "6", "pods", "3")
		setHRQ(ctx, barHRQName, barName, "secrets", "100", "cpu", "50")
		setHRQ(ctx, bazHRQName, bazName, "pods", "1")
		// Simulate the K8s ResourceQuota controller to update usages. bar and baz
		// are foo's children, so they have "secrets" and "pods" in their RQ singleton.
		updateRQUsage(ctx, fooName, "secrets", "0", "pods", "0")
		updateRQUsage(ctx, barName, "secrets", "0", "cpu", "0", "pods", "0")
		updateRQUsage(ctx, bazName, "secrets", "0", "pods", "0")

		// Increase secret counts from 0 to 10 in baz. Note that the number of secrets is theoretically
		// limited to 6 (since baz is a child of foo), but the webhooks aren't actually running in this
		// test so we can exceed those limits.
		updateRQUsage(ctx, bazName, "secrets", "10")

		// Check in-memory resource usages for baz (should have increased).
		Eventually(forestGetLocalUsages(bazName)).Should(equalRL("secrets", "10", "pods", "0"))
		Eventually(forestGetSubtreeUsages(bazName)).Should(equalRL("secrets", "10", "pods", "0"))

		// Check in-memory resource usages for bar (unaffected by what's going on in baz).
		Eventually(forestGetLocalUsages(barName)).Should(equalRL("secrets", "0", "cpu", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(barName)).Should(equalRL("secrets", "0", "cpu", "0", "pods", "0"))

		// Check in-memory resource usages for foo (local usage is unaffected;
		// subtree usage has gone up since baz is a child).
		Eventually(forestGetLocalUsages(fooName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(fooName)).Should(equalRL("secrets", "10", "pods", "0"))

		// Decrease secret counts from 10 to 0 in baz.
		updateRQUsage(ctx, bazName, "secrets", "0")

		// Check in-memory resource usages for baz.
		Eventually(forestGetLocalUsages(bazName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(bazName)).Should(equalRL("secrets", "0", "pods", "0"))

		// Check in-memory resource usages for bar.
		Eventually(forestGetLocalUsages(barName)).Should(equalRL("secrets", "0", "cpu", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(barName)).Should(equalRL("secrets", "0", "cpu", "0", "pods", "0"))

		// Check in-memory resource usages for foo.
		Eventually(forestGetLocalUsages(fooName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(fooName)).Should(equalRL("secrets", "0", "pods", "0"))

		// Additionally testing not limited resource consumption: increase cpu usage
		// in baz, which is not limited in foo or baz itself. Make sure the in-memory
		// subtree usage for foo and the local and subtree usage for baz are zero.
		updateRQUsage(ctx, bazName, "cpu", "1")
		Eventually(forestGetLocalUsages(fooName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(fooName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(forestGetLocalUsages(bazName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(forestGetSubtreeUsages(bazName)).Should(equalRL("secrets", "0", "pods", "0"))
	})

	// This is simulating the RQ status change happened in the RQStatusValidator,
	// where the forest is updated while the HRQs are not yet enqueued.
	It("should enqueue HRQs even if the forest usages (updated by RQStatusValidator) is the same as the RQ usages", func() {
		setHRQ(ctx, fooHRQName, fooName, "secrets", "6", "pods", "3")
		setHRQ(ctx, barHRQName, barName, "secrets", "100", "cpu", "50")
		setHRQ(ctx, bazHRQName, bazName, "pods", "1")
		// Simulate the K8s ResourceQuota controller to update usages. bar and baz
		// are foo's children, so they have "secrets" and "pods" in their RQ singleton.
		updateRQUsage(ctx, fooName, "secrets", "0", "pods", "0")
		updateRQUsage(ctx, barName, "secrets", "0", "cpu", "0", "pods", "0")
		updateRQUsage(ctx, bazName, "secrets", "0", "pods", "0")
		// Make sure the above RQ reconciliations are finished before proceeding.
		Eventually(getHRQUsed(ctx, fooName, fooHRQName)).Should(equalRL("secrets", "0", "pods", "0"))
		Eventually(getHRQUsed(ctx, barName, barHRQName)).Should(equalRL("secrets", "0", "cpu", "0"))
		Eventually(getHRQUsed(ctx, bazName, bazHRQName)).Should(equalRL("pods", "0"))

		// Simulate RQStatusValidator - update usages in the forest and the RQ
		// object. Since updating RQ usages triggers the RQ reconciliation that
		// enqueues HRQs to update usages, let's check the foo HRQ before the RQ
		// update to make sure it's not updated before the RQ reconciliation.
		forestUseResource(bazName, "secrets", "10", "pods", "0")
		Eventually(forestGetSubtreeUsages(fooName)).Should(equalRL("secrets", "10", "pods", "0"))
		// Use Consistently here because the HRQs should not be enqueued to update
		// usages yet.
		Consistently(getHRQUsed(ctx, fooName, fooHRQName)).Should(equalRL("secrets", "0", "pods", "0"))
		// Simulate the K8s ResourceQuota admission controller to update usages
		// after validating the request.
		updateRQUsage(ctx, bazName, "secrets", "10")

		// Verify the HRQ is enqueued to update the usage.
		Eventually(getHRQUsed(ctx, fooName, fooHRQName)).Should(equalRL("secrets", "10", "pods", "0"))
	})

	It("should set cleanup label on the singletons", func() {
		setHRQ(ctx, fooHRQName, fooName, "secrets", "6", "pods", "3")

		Eventually(rqSingletonHasCleanupLabel(ctx, fooName)).Should(Equal(true))
		Eventually(rqSingletonHasCleanupLabel(ctx, barName)).Should(Equal(true))
		Eventually(rqSingletonHasCleanupLabel(ctx, bazName)).Should(Equal(true))
	})

	It("should add non-propagate exception annotation to all the RQ singletons", func() {
		// Since foo is the ancestor of both bar and baz, we just need to create one
		// hrq in the foo namespace for this test.
		setHRQ(ctx, fooHRQName, fooName, "secrets", "6", "pods", "3")

		// All RQ singletons in foo, bar, baz should have the non-propagate annotation.
		Eventually(rqSingletonHasNonPropagateAnnotation(ctx, fooName)).Should(Equal(true))
		Eventually(rqSingletonHasNonPropagateAnnotation(ctx, barName)).Should(Equal(true))
		Eventually(rqSingletonHasNonPropagateAnnotation(ctx, bazName)).Should(Equal(true))
	})
})

// rlMatcher implements GomegaMatcher: https://onsi.github.io/gomega/#adding-your-own-matchers
type rlMatcher struct {
	want v1.ResourceList
	got  v1.ResourceList
}

// equalRL is a replacement for the Equal() Gomega matcher but automatically constructs a resource
// list (RL) from its arguments, which are interpretted by argsToResourceList.
func equalRL(args ...string) *rlMatcher {
	return &rlMatcher{want: argsToResourceList(1, args...)}
}

func (r *rlMatcher) Match(actual interface{}) (bool, error) {
	got, ok := actual.(v1.ResourceList)
	if !ok {
		return false, fmt.Errorf("Not a resource list: %+v", actual)
	}
	r.got = got
	return utils.Equals(got, r.want), nil
}

func (r *rlMatcher) FailureMessage(_ interface{}) string {
	return fmt.Sprintf("Got: %+v\nWant: %+v", r.got, r.want)
}

func (r *rlMatcher) NegatedFailureMessage(_ interface{}) string {
	return fmt.Sprintf("Did not want: %+v", r.want)
}

func forestGetLocalUsages(ns string) func() v1.ResourceList {
	TestForest.Lock()
	defer TestForest.Unlock()
	return func() v1.ResourceList {
		return TestForest.Get(ns).GetLocalUsages()
	}
}

func forestGetSubtreeUsages(ns string) func() v1.ResourceList {
	TestForest.Lock()
	defer TestForest.Unlock()
	return func() v1.ResourceList {
		return TestForest.Get(ns).GetSubtreeUsages()
	}
}

func forestUseResource(ns string, args ...string) {
	TestForest.Lock()
	defer TestForest.Unlock()
	TestForest.Get(ns).UseResources(argsToResourceList(0, args...))
}

// updateRQUsages updates the resource usages on the per-namespace RQ in this
// test environment. It can simulate the usage changes coming from the K8s
// ResourceQuota controller or K8s ResourceQuota admission controller. If a
// resource is limited in the RQ but not mentioned when calling this function,
// it is left unchanged (i.e. this is a merge operation at the overall status
// level, not a replacement of the entire status).
func updateRQUsage(ctx context.Context, ns string, args ...string) {
	nsn := types.NamespacedName{Namespace: ns, Name: hrq.ResourceQuotaSingleton}
	EventuallyWithOffset(1, func() error {
		rq := &v1.ResourceQuota{}
		if err := K8sClient.Get(ctx, nsn, rq); err != nil {
			return err
		}
		if rq.Status.Used == nil {
			rq.Status.Used = v1.ResourceList{}
		}
		for k, v := range argsToResourceList(1, args...) {
			rq.Status.Used[k] = v
		}
		return K8sClient.Status().Update(ctx, rq)
	}).Should(Succeed(), "Updating usage in %s to %v", ns, args)
}

// setHRQ creates or replaces an existing HRQ with the given resource limits. Any existing spec is
// replaced by this function.
func setHRQ(ctx context.Context, nm, ns string, args ...string) {
	nsn := types.NamespacedName{Namespace: ns, Name: nm}
	hrq := &api.HierarchicalResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nm,
			Namespace: ns,
		},
	}
	EventuallyWithOffset(1, func() error {
		err := K8sClient.Get(ctx, nsn, hrq)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}).Should(Succeed(), "While checking if HRQ %s/%s already exists", ns, nm)

	hrq.Spec.Hard = argsToResourceList(1, args...)

	EventuallyWithOffset(1, func() error {
		if hrq.CreationTimestamp.IsZero() {
			err := K8sClient.Create(ctx, hrq)
			if err == nil {
				createdHRQs = append(createdHRQs, hrq)
			}
			return err
		} else {
			return K8sClient.Update(ctx, hrq)
		}
	}).Should(Succeed(), "While updating HRQ; %+v", hrq)
}

// argsToResourceList provides a convenient way to specify a resource list in these tests by
// interpretting even-numbered args as resource names (e.g. "secrets") and odd-valued args as
// quantities (e.g. "5", "1Gb", etc).
func argsToResourceList(offset int, args ...string) v1.ResourceList {
	list := map[v1.ResourceName]resource.Quantity{}
	ExpectWithOffset(offset+1, len(args)%2).Should(Equal(0), "Need even number of arguments, not %d", len(args))
	for i := 0; i < len(args); i += 2 {
		list[v1.ResourceName(args[i])] = resource.MustParse(args[i+1])
	}
	return list
}

func cleanupHRQObjects(ctx context.Context) {
	for _, obj := range createdHRQs {
		GinkgoT().Logf("Deleting HRQ object: %s/%s", obj.Namespace, obj.Name)
		EventuallyWithOffset(1, func() error {
			err := K8sClient.Delete(ctx, obj)
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}).Should(Succeed(), "While deleting object %s/%s", obj.GetNamespace(), obj.GetName())
	}
	createdHRQs = []*api.HierarchicalResourceQuota{}
}

func rqSingletonHasCleanupLabel(ctx context.Context, ns string) func() bool {
	return func() bool {
		nsn := types.NamespacedName{Namespace: ns, Name: hrq.ResourceQuotaSingleton}
		inst := &v1.ResourceQuota{}
		if err := K8sClient.Get(ctx, nsn, inst); err != nil {
			return false
		}
		if set, ok := metadata.GetLabel(inst, api.HRQLabelCleanup); ok {
			return set == "true"
		}
		return false
	}
}

func rqSingletonHasNonPropagateAnnotation(ctx context.Context, ns string) func() bool {
	return func() bool {
		nsn := types.NamespacedName{Namespace: ns, Name: hrq.ResourceQuotaSingleton}
		inst := &v1.ResourceQuota{}
		if err := K8sClient.Get(ctx, nsn, inst); err != nil {
			return false
		}
		if set, ok := metadata.GetAnnotation(inst, api.NonPropagateAnnotation); ok {
			return set == "true"
		}
		return false
	}
}
