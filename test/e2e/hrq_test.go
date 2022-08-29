package e2e

import (
	"io/ioutil"
	"math/rand"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/hierarchical-namespaces/pkg/testutils"
)

// This test primarily uses PVCs to test the quota system (with a few tests of pods). We used to use
// Secrets and ConfigMaps, but K8s and GKE keep on changing the number of default objects of these
// types that it puts in every namespace. PVCs seem to be more stable and are well-documented in
// https://kubernetes.io/docs/tasks/administer-cluster/quota-api-object/.

const (
	prefix = "hrq-test-"
	nsA = prefix+"a"
	nsB = prefix+"b"
	nsC = prefix+"c"
	nsD = prefix+"d"

	propagationTime = 5
	resourceQuotaSingleton = "hrq.hnc.x-k8s.io"
)

// HRQ tests are pending (disabled) until we turn them on in all the default manifests
var _ = PDescribe("Hierarchical Resource Quota", func() {
	BeforeEach(func() {
		rand.Seed(time.Now().UnixNano())
		CleanupTestNamespaces()
	})

	AfterEach(func() {
		CleanupTestNamespaces()
	})

	It("should create RQs with correct limits in the descendants (including itself) for HRQs", func() {
		// set up
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)

		// test
		setHRQ("a-hrq", nsA, "persistentvolumeclaims", "3")
		setHRQ("b-hrq", nsB, "secrets", "2")

		// verify
		FieldShouldContain("resourcequota", nsA, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3")
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3 secrets:2")
	})

	It("should deny the request if the HRQ has invalid quota types", func() {
		// set up
		CreateNamespace(nsA)

		// test and verify ("foobar" is an invalid quota type)
		shouldNotSetHRQ("a-hrq", nsA, "persistentvolumeclaims", "3", "foobar", "1")
	})

	It("should remove obsolete (empty) RQ singletons if there's no longer an HRQ in the ancestor", func() {
		// set up namespaces
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up HRQs and verify descendant RQs
		setHRQ("a-hrq", nsA, "persistentvolumeclaims", "3")
		setHRQ("b-hrq", nsB, "secrets", "2")
		FieldShouldContain("resourcequota", nsA, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3")
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3 secrets:2")

		// test deleting the HRQ in namespace 'a'
		MustRun("kubectl delete hrq -n", nsA, "a-hrq")

		// verify the RQ singleton in namespace 'a' is gone
		RunShouldNotContain(resourceQuotaSingleton, propagationTime, "kubectl get resourcequota -n", nsA)
		// verity the RQ singleton in namespace 'b' still exists but the limits are
		// updated
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "secrets:2")
		FieldShouldNotContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims")
	})

	It("should update descendants' (including itself's) RQ limits if an ancestor HRQ changes", func() {
		// set up namespaces
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up an HRQ and verify descendant RQs
		setHRQ("a-hrq", nsA, "persistentvolumeclaims", "3")
		FieldShouldContain("resourcequota", nsA, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3")
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3")

		// test updating the HRQ
		setHRQ("a-hrq", nsA, "persistentvolumeclaims", "2", "secrets", "2")

		// verify the RQ limits are updated
		FieldShouldContain("resourcequota", nsA, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:2 secrets:2")
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:2 secrets:2")
	})

	It("should update ancestor HRQ usages if limited resource is consumed/deleted in a descendant (including itself)", func() {
		// set up namespaces
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up an HRQ
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "3")

		// test consuming a resource in a descendant
		Eventually(createPVC("b-pvc", nsB)).Should(Succeed())
		// verify the HRQ usages are updated
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")

		// test consuming a resource in the same namespace (itself) with HRQ
		Eventually(createPVC("a-pvc", nsA)).Should(Succeed())
		// verify the HRQ usages are updated
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:2")

		// test deleting a resource in a descendant
		MustRun("kubectl delete persistentvolumeclaim b-pvc -n", nsB)
		// verify the HRQ usages are updated
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")
	})

	It("should update ancestor HRQ usages if a descendant namespace with limited resources is deleted", func() {
		// set up namespaces
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up an HRQ
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "3")
		// verify usage updates after consuming limited resources in the descendants
		Eventually(createPVC("b-pvc", nsB)).Should(Succeed())
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")

		// test deleting the namespace with usages
		MustRun("kubectl delete subns", nsB, "-n", nsA)

		// verify the HRQ usages are updated (restored)
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:0")
	})

	It("should update ancestor HRQ usages if a descendant namespace with limited resources is moved out of subtree", func() {
		// set up namespaces and subnamespaces
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		CreateSubnamespace(nsC, nsB)
		CreateNamespace(nsD)

		// set up an HRQ on the root namespaces and on the child namespace
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "5")
		setHRQ("a-hrq-secrets", nsA, "secrets", "5")

		setHRQ("a-hrq-persistentvolumeclaims", nsC, "persistentvolumeclaims", "3")
		setHRQ("a-hrq-secrets", nsC, "secrets", "3")

		setHRQ("a-hrq-persistentvolumeclaims", nsD, "persistentvolumeclaims", "1")
		setHRQ("a-hrq-secrets", nsD, "secrets", "1")

		// verify usage updates after consuming limited resources in the descendants
		Eventually(createPVC("b-pvc", nsB)).Should(Succeed())
		Eventually(createSecret("b-secret", nsB)).Should(Succeed())

		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")
		FieldShouldContain("hrq", nsA, "a-hrq-secrets", ".status.used", "secrets:1")

		Eventually(createPVC("d-pvc", nsD)).Should(Succeed())
		Eventually(createSecret("d-secret", nsD)).Should(Succeed())

		// make full namespace the child of a subnamespace
		MustRun("kubectl hns set", nsD, "--parent", nsC)

		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:2")
		FieldShouldContain("hrq", nsA, "a-hrq-secrets", ".status.used", "secrets:2")
		FieldShouldContain("hrq", nsC, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")
		FieldShouldContain("hrq", nsC, "a-hrq-secrets", ".status.used", "secrets:1")
		FieldShouldContain("hrq", nsD, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")
		FieldShouldContain("hrq", nsD, "a-hrq-secrets", ".status.used", "secrets:1")

		// test moving out of subree the namespace with usages
		MustRun("kubectl hns set --root", nsD)

		// verify the HRQ usages are updated
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")
		FieldShouldContain("hrq", nsA, "a-hrq-secrets", ".status.used", "secrets:1")
		FieldShouldContain("hrq", nsC, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:0")
		FieldShouldContain("hrq", nsC, "a-hrq-secrets", ".status.used", "secrets:0")
		FieldShouldContain("hrq", nsD, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:1")
		FieldShouldContain("hrq", nsD, "a-hrq-secrets", ".status.used", "secrets:1")
	})

	It("should update descendant RQ limits or delete them if a namespace with HRQs is deleted", func() {
		// set up namespaces - 'a' as the parent of 'b' and 'c'.
		CreateNamespace(nsA)
		CreateNamespace(nsB)
		CreateNamespace(nsC)
		MustRun("kubectl hns set -p", nsA, nsB)
		MustRun("kubectl hns set -p", nsA, nsC)

		// set up an HRQ in namespace 'a' and 'b'
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "3")
		setHRQ("b-hrq-secrets", nsB, "secrets", "2")
		// verify RQ singletons in the descendants 'b' and 'c'
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3 secrets:2")
		FieldShouldContain("resourcequota", nsC, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims:3")

		// Make the child namespaces roots
		MustRun("kubectl hns set -r", nsB)
		MustRun("kubectl hns set -r", nsC)

		// verify the RQ is updated in namespace 'b' and removed in namespace 'c'
		FieldShouldNotContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "persistentvolumeclaims")
		FieldShouldContain("resourcequota", nsB, resourceQuotaSingleton, ".spec.hard", "secrets:2")
		MustNotRun("kubectl get resourcequota -n", nsC, resourceQuotaSingleton)
	})

	It("should recreate RQ singleton if user deletes it", func() {
		// set up namespaces
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up ancestor HRQ and verify descendant RQs
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "3")
		MustRun("kubectl get resourcequota -n", nsA, resourceQuotaSingleton)
		MustRun("kubectl get resourcequota -n", nsB, resourceQuotaSingleton)

		// test deleting the RQ singleton in the descendant
		MustRun("kubectl delete resourcequota -n", nsB, resourceQuotaSingleton)

		// verify the RQ singleton is recreated
		MustRun("kubectl get resourcequota -n", nsB, resourceQuotaSingleton)
	})

	It("shouldn't allow consuming resources violating HRQs", func() {
		// set up namespaces a <- b (a is the parent)
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up an HRQ with limits of 1 persistentvolumeclaims in namespace a
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "1")

		// verify allowing creating 1 persistentvolumeclaims in namespace b
		Eventually(createPVC("b-pvc", nsB)).Should(Succeed())
		// verify denying creating any more persistentvolumeclaims
		Expect(createPVC("b-pvc-2", nsB)()).ShouldNot(Succeed())
		Expect(createPVC("a-pvc", nsA)()).ShouldNot(Succeed())

		// test deleting the only persistentvolumeclaims
		MustRun("kubectl delete persistentvolumeclaim b-pvc -n", nsB)
		// verify allowing creating 1 persistentvolumeclaims in namespace a
		Eventually(createPVC("a-pvc", nsA)).Should(Succeed())
		// verify denying creating any more persistentvolumeclaims again
		Expect(createPVC("a-pvc-2", nsA)()).ShouldNot(Succeed())
		Expect(createPVC("b-pvc", nsB)()).ShouldNot(Succeed())
	})

	It("should leave the resource consumption as is if it violates HRQs before HRQs are created", func() {
		// set up namespaces a <- b (a is the parent)
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		// set up 1 persistentvolumeclaims in each namespace
		Eventually(createPVC("b-pvc", nsB)).Should(Succeed())
		Eventually(createPVC("a-pvc", nsA)).Should(Succeed())
		// set up an HRQ with limits of 1 persistentvolumeclaims in namespace a
		setHRQ("a-hrq-persistentvolumeclaims", nsA, "persistentvolumeclaims", "1")

		// verify two existing persistentvolumeclaims still exist
		MustRun("kubectl get persistentvolumeclaim b-pvc -n", nsB)
		MustRun("kubectl get persistentvolumeclaim a-pvc -n", nsA)
		// verify the HRQ usage is 2 even if the limit is 1
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".status.used", "persistentvolumeclaims:2")
		FieldShouldContain("hrq", nsA, "a-hrq-persistentvolumeclaims", ".spec.hard", "persistentvolumeclaims:1")
	})

	It("should not allow creating a pod if the pod cpu/memory usages violate HRQs", func() {
		// Set up namespaces - 'a' as the parent of 'b' and 'c'.
		CreateNamespace(nsA)
		CreateSubnamespace(nsB, nsA)
		CreateSubnamespace(nsC, nsA)
		// Set up an HRQ with limits of 200Mi memory and 300m cpu in namespace a.
		setHRQ("a-hrq", nsA, "memory", "200Mi", "cpu", "300m")

		// Should allow creating a pod under limit in namespace b.
		createPod("pod1", nsB, "memory 200Mi cpu 300m", "memory 100Mi cpu 150m")

		// Verify the HRQ usages are updated.
		FieldShouldContain("hrq", nsA, "a-hrq", ".status.used", "memory:100Mi")
		FieldShouldContain("hrq", nsA, "a-hrq", ".status.used", "cpu:150m")

		// Try creating a pod in namespace c (b's sibling who is also subject to
		// the HRQ in namespace a).
		// Should reject creating a pod exceeding cpu limits (250m + 150m exceeds 300m).
		shouldNotCreatePod("pod2", nsC, "memory 200Mi cpu 500m", "memory 100Mi cpu 250m")
		// Should reject creating a pod exceeding memory limits (150Mi + 100Mi exceeds 200Mi).
		shouldNotCreatePod("pod2", nsC, "memory 300Mi cpu 300m", "memory 150Mi cpu 150m")
		// Should reject creating a pod exceeding both memory and cpu limits.
		shouldNotCreatePod("pod2", nsC, "memory 300Mi cpu 500m", "memory 150Mi cpu 250m")
		// Should allow creating another pod under limit.
		createPod("pod2", nsC, "memory 200Mi cpu 300m", "memory 100Mi cpu 150m")
	})
})

func generateHRQManifest(nm, nsnm string, args ...string) string {
	return `# temp file created by hrq_test.go
apiVersion: hnc.x-k8s.io/v1alpha2
kind: HierarchicalResourceQuota
metadata:
  name: ` + nm + `
  namespace: ` + nsnm + `
spec:
  hard:` + argsToResourceListString(2, args...)
}

func generatePodManifest(nm, nsnm, limits, requests string) string {
	l := argsToResourceListString(4, strings.Split(limits, " ")...)
	r := argsToResourceListString(4, strings.Split(requests, " ")...)
	return `# temp file created by hrq_test.go
apiVersion: v1
kind: Pod
metadata:
  name: ` + nm + `
  namespace: ` + nsnm + `
spec:
  containers:
  - name: quota-mem-cpu
    image: nginx
    resources:
      limits:` + l + `
      requests:` + r
}

// createPod creates a pod with the given container memory and cpu limits and
// requests.
func createPod(nm, nsnm, limits, requests string) {
	pod := generatePodManifest(nm, nsnm, limits, requests)
	MustApplyYAML(pod)
	RunShouldContain(nm, propagationTime, "kubectl get pod -n", nsnm)
}

func createPVC(nm, nsnm string) func() error {
	return func() error {
		pvc := `# temp file created by hrq_test.go
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: `+nm+`
  namespace: `+nsnm+`
spec:
  storageClassName: manual
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi`
		fn := writeTempFile(pvc)
		GinkgoT().Log("Wrote "+fn+":\n"+pvc)
		defer removeFile(fn)
		return TryRun("kubectl apply -f", fn)
	}
}

func createSecret(nm, nsnm string) func() error {
	return func() error {
		secret := `# temp file created by hrq_test.go
apiVersion: v1
kind: Secret
metadata:
  name: ` + nm + `
  namespace: ` + nsnm + `
data:
  key: YmFyCg==`
		fn := writeTempFile(secret)
		GinkgoT().Log("Wrote " + fn + ":\n" + secret)
		defer removeFile(fn)
		return TryRun("kubectl apply -f", fn)
	}
}

func writeTempFile(cxt string) string {
	f, err := ioutil.TempFile(os.TempDir(), "e2e-test-*.yaml")
	Expect(err).Should(BeNil())
	defer f.Close()
	f.WriteString(cxt)
	return f.Name()
}

func removeFile(path string) {
	Expect(os.Remove(path)).Should(BeNil())
}

// shouldNotCreatePod should not be able to create the pod with the given
// container memory and cpu limits and requests.
func shouldNotCreatePod(nm, nsnm, limits, requests string) {
	pod := generatePodManifest(nm, nsnm, limits, requests)
	MustNotApplyYAML(pod)
	RunShouldNotContain(nm, propagationTime, "kubectl get pod -n", nsnm)
}

// setHRQ creates/updates an HRQ with a given name in the given namespace with
// hard limits.
func setHRQ(nm, nsnm string, args ...string) {
	hrq := generateHRQManifest(nm, nsnm, args...)
	MustApplyYAML(hrq)
	RunShouldContain(nm, propagationTime, "kubectl get hrq -n", nsnm)
}

// shouldNotSetHRQ should not be able to create the HRQ with the given resources.
func shouldNotSetHRQ(nm, nsnm string, args ...string) {
	hrq := generateHRQManifest(nm, nsnm, args...)
	MustNotApplyYAML(hrq)
	RunShouldNotContain(nm, propagationTime, "kubectl get hrq -n", nsnm)
}

// argsToResourceListString provides a convenient way to specify a resource list
// in hard limits/usages for HRQ or RQ instances, or limits/requests for pod
// containers by interpreting even-numbered args as resource names (e.g.
// "secrets") and odd-valued args as quantities (e.g. "5", "1Gb", etc). The
// level controls how much indention in the output (0 indicates no indention).
func argsToResourceListString(level int, args ...string) string {
	Expect(len(args)%2).Should(Equal(0), "Need even number of arguments, not %d", len(args))
	rl := ``
	for i := 0; i < len(args); i += 2 {
		rl += `
` + strings.Repeat("  ", level) + args[i] + `: "` + args[i+1] + `"`
	}
	return rl
}
