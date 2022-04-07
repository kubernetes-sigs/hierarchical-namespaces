package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "sigs.k8s.io/hierarchical-namespaces/pkg/testutils"
)

var _ = Describe("HNS set-config", func() {
	It("Should use '--force' flag to change from 'Ignore' to 'Propagate'", func() {
		MustRun("kubectl hns config set-resource secrets --mode Ignore")
		MustNotRun("kubectl hns config set-resource secrets --mode Propagate")
		MustRun("kubectl hns config set-resource secrets --mode Propagate --force")
		// check that we don't need '--force' flag when changing it back
		MustRun("kubectl hns config set-resource secrets --mode Ignore")
	})
})
