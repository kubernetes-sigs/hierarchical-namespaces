package e2e

import (
	"os/exec"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/hierarchical-namespaces/pkg/testutils"
)

var _ = Describe("Namespace", func() {
	const (
		prefix = namspacePrefix + "namespace-"
		nsA    = prefix + "a"
		nsB    = prefix + "b"
		wrong_included_file = "wrong_included_namespace.yaml"
	)

	BeforeEach(func() {
		CleanupTestNamespaces()
	})

	AfterEach(func() {
		CleanupTestNamespaces()
	})

	It("should create and delete a namespace", func() {
		// set up
		CreateNamespace(nsA)
		MustRun("kubectl get ns", nsA)

		// test
		MustRun("kubectl delete ns", nsA)

		// verify
		MustNotRun("kubectl get ns", nsA)
	})

	It("should properly add included-namespace label and reject illegal changes", func() {
		// Reject creating a non-excluded namespace with illegal included-namespace
		// label value.
		MustNotRun("kubectl apply -f ", wrong_included_file)

		// Create a namespace.
		CreateNamespace(nsA)
		MustRun("kubectl get ns", nsA)

		// Verify the included-namespace label is added.
		RunShouldContain("hnc.x-k8s.io/included-namespace=true", 2, "kubectl describe ns", nsA)

		// Reject updating a non-excluded namespace with illegal included-namespace
		// label value.
		MustNotRun("kubectl apply -f ", wrong_included_file)
		MustNotRun("kubectl label ns kube-system hnc.x-k8s.io/included-namespace=false --overwrite")

		// Reject adding included-namespace label on an excluded-namespace.
		MustNotRun("kubectl label ns kube-system hnc.x-k8s.io/included-namespace=true --overwrite")
		MustNotRun("kubectl label ns kube-system hnc.x-k8s.io/included-namespace=false --overwrite")
	})

	It("should have 'ParentMissing' condition on orphaned namespace", func() {
		// set up
		CreateNamespace(nsA)
		CreateNamespace(nsB)
		MustRun("kubectl hns set", nsB, "--parent", nsA)
		MustRun("kubectl delete ns", nsA)

		// "b" should have 'ParentMissing' condition. The command to use:
		// kubectl get hierarchyconfigurations.hnc.x-k8s.io hierarchy -n b -o jsonpath='{.status.conditions..code}'
		out, err := exec.Command("kubectl", "get", "hierarchyconfigurations.hnc.x-k8s.io", "hierarchy", "-n", nsB, "-o", "jsonpath='{.status.conditions..reason}'").Output()
		// Convert []byte to string and remove the quotes to get the condition value.
		condition := string(out)[1 : len(out)-1]
		Expect(err).Should(BeNil())
		Expect(condition).Should(Equal("ParentMissing"))
	})
})
