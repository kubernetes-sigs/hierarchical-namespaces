package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"

	. "sigs.k8s.io/hierarchical-namespaces/pkg/testutils"
)

var _ = Describe("List Resources", func() {
	const (
		parent1       = namspacePrefix + "parent1"
		parent2       = namspacePrefix + "parent2"
		child1a       = namspacePrefix + "child1a"
		child1b       = namspacePrefix + "child1b"
		child2a       = namspacePrefix + "child2a"
		grandchild1ai = namspacePrefix + "grandchild1ai"
	)

	BeforeEach(func() {
		CleanupTestNamespaces()
		CreateNamespace(parent1)
		CreateNamespace(parent2)
		CreateSubnamespace(child1a, parent1)
		CreateSubnamespace(child1b, parent1)
		CreateSubnamespace(child2a, parent2)
		CreateSubnamespace(grandchild1ai, child1a)
		// wait for all the descendents to be in the tree
		RunShouldContain(grandchild1ai, 5, "kubectl hns tree", parent1)
	})

	AfterEach(func() {
		CleanupTestNamespaces()
	})

	It("should list resources", func() {
		createEmptySecret(parent1, "s-parent1")
		createEmptySecret(child1a, "s-child1a")
		createEmptySecret(child1b, "s-child1b")
		createEmptySecret(grandchild1ai, "s-grandchild1ai")
		createEmptySecret(child2a, "s-child2a")

		// verify
		// list resources under one tree
		RunShouldContainMultiple([]string{"s-parent1", "s-child1a", "s-child1b", "s-grandchild1ai"}, 5, "kubectl hns --namespace", parent1, "get secret")
		RunShouldNotContain("s-child2a", 5, "kubectl hns --namespace", parent1, "get secret")

		// list resources under another tree
		RunShouldContain("s-child2a", 5, "kubectl hns --namespace", parent2, "get secret")
		RunShouldNotContainMultiple([]string{"s-parent1", "s-child1a", "s-child1b", "s-grandchild1ai"}, 5, "kubectl hns --namespace", parent2, "get secret")

		// list all resources
		RunShouldContainMultiple([]string{"s-parent1", "s-child1a", "s-child1b", "s-grandchild1ai", "s-child2a"}, 5, "kubectl hns get secret --all-namespaces")
	})

	It("should watch resources", func() {
		go func() {
			defer GinkgoRecover()
			interval := 1 * time.Second
			time.Sleep(interval)
			createEmptySecret(parent1, "s-parent1")
			time.Sleep(interval)
			createEmptySecret(child1a, "s-child1a")
			time.Sleep(interval)
			createEmptySecret(child1b, "s-child1b")
			time.Sleep(interval)
			createEmptySecret(grandchild1ai, "s-grandchild1ai")
			time.Sleep(interval)
			createEmptySecret(child2a, "s-child2a")
		}()

		// verify
		cmd := []string{"kubectl hns --namespace", parent1, "get secret --watch"}
		wantStrings := []string{"s-parent1", "s-child1a", "s-child1b", "s-grandchild1ai"}
		RunShouldContainMultipleWithTimeout(wantStrings, 15, 15, cmd...)
	})
})

func createEmptySecret(namespace, name string) {
	MustRun("kubectl --namespace", namespace, "create secret generic", name)
}
