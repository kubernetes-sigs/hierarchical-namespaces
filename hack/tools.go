//go:build tools
// +build tools

//
// This is used to ensure that various tools are included in the /vendor directory.  See
// https://stackoverflow.com/questions/52428230/how-do-go-modules-work-with-installable-commands.
package hack

import (
	_ "honnef.co/go/tools/cmd/staticcheck"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"
)
