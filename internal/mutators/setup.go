package mutators

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// Create creates the mutator. This function is called from main.go.
func Create(mgr ctrl.Manager) {
	// Create mutator for namespace `included-namespace` label.
	mgr.GetWebhookServer().Register(NamespaceMutatorServingPath, &webhook.Admission{Handler: &Namespace{
		Log: ctrl.Log.WithName("mutators").WithName("Namespace"),
	}})
}
