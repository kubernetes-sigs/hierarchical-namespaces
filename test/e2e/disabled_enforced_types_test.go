package e2e

import (
	. "github.com/onsi/ginkgo"
	. "sigs.k8s.io/hierarchical-namespaces/pkg/testutils"
)

var _ = Describe("With disabled enforced types", func() {

	const (
		nsParent = "parent"
		nsChild  = "child"
	)

	BeforeEach(func() {
		CheckHNCPath()
		AddDisableEnforcedTypesArg()
		CleanupTestNamespaces()
	})

	AfterEach(func() {
		CleanupTestNamespaces()
		RecoverHNC()
	})

	It("should have no synchronized resources configured by default", func() {
		// check for roles and rolebindings not being present by default
		RunShouldNotContainMultiple([]string{"roles", "rolebindings"}, defTimeout, "kubectl hns config describe")

		// create roles propagation
		MustRun("kubectl hns config set-resource roles --group rbac.authorization.k8s.io --mode Propagate")
		RunShouldContain("roles", defTimeout, "kubectl hns config describe")

		// create secrets propagation
		MustRun("kubectl hns config set-resource secrets --mode Propagate")
		RunShouldContain("secrets", defTimeout, "kubectl hns config describe")

		// remove it again and recheck that it's gone
		MustRun("kubectl hns config delete-type --group rbac.authorization.k8s.io --resource roles")
		RunShouldNotContainMultiple([]string{"roles", "rolebindings"}, defTimeout, "kubectl hns config describe")
	})

	It("should synchronize roles if asked to", func() {
		// create namespaces and set relation
		CreateNamespace(nsParent)
		CreateNamespace(nsChild)
		MustRun("kubectl hns set", nsChild, "--parent", nsParent)

		// create role and check that it's not propagated
		MustRun("kubectl -n", nsParent, "create role", nsParent+"-sre", "--verb=update --resource=deployments")
		RunShouldContain("No resources found in "+nsChild, defTimeout, "kubectl -n", nsChild, "get roles")

		// check for roles and rolebindings not being present by default
		RunShouldNotContainMultiple([]string{"roles", "rolebindings"}, defTimeout, "kubectl hns config describe")

		// create roles propagation
		MustRun("kubectl hns config set-resource roles --group rbac.authorization.k8s.io --mode Propagate")
		RunShouldContain("roles", defTimeout, "kubectl hns config describe")

		RunShouldContain("Parent: "+nsParent, defTimeout, "kubectl hns describe", nsChild)
		RunShouldContain(nsChild, propogationTimeout, "kubectl hns describe", nsParent)
		RunShouldContain(nsParent+"-sre", propogationTimeout, "kubectl -n", nsChild, "get roles")

		// remove it again and recheck that it's gone
		MustRun("kubectl hns config delete-type --group rbac.authorization.k8s.io --resource roles")
		RunShouldNotContainMultiple([]string{"roles", "rolebindings"}, defTimeout, "kubectl hns config describe")
	})
})
