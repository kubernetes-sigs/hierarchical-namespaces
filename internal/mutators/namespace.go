package mutators

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
)

// NamespaceMutatorServingPath is where the mutator will run. Must be kept in
// sync with the kubebuilder markers below.
const (
	NamespaceMutatorServingPath = "/mutate-namespace"
)

// Note: the mutating webhook FAILS OPEN. This means that if the webhook goes
// down, all further changes are allowed. (An empty line has to be kept below
// the kubebuilder marker for the controller-gen to generate manifests.)
//
// +kubebuilder:webhook:admissionReviewVersions=v1,path=/mutate-namespace,mutating=true,failurePolicy=ignore,groups="",resources=namespaces,sideEffects=None,verbs=create;update,versions=v1,name=namespacelabel.hnc.x-k8s.io

type Namespace struct {
	Log     logr.Logger
	decoder *admission.Decoder
}

// Handle implements the mutating webhook.
func (m *Namespace) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := m.Log.WithValues("namespace", req.Name)
	ns := &corev1.Namespace{}
	err := m.decoder.Decode(req, ns)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	m.handle(log, ns)
	marshaledNS, err := json.Marshal(ns)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledNS)
}

// handle implements the non-boilerplate logic of this mutator, allowing it to
// be more easily unit tested (ie without constructing a full admission.Request).
// Currently, we only add `included-namespace` label to non-excluded namespaces
// if the label is missing.
func (m *Namespace) handle(log logr.Logger, ns *corev1.Namespace) {
	// Early exit if the namespace is excluded.
	if config.ExcludedNamespaces[ns.Name] {
		return
	}

	// Add label if the namespace doesn't have it.
	if _, hasLabel := ns.Labels[api.LabelIncludedNamespace]; !hasLabel {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		log.Info("Not an excluded namespace; set included-namespace label.")
		ns.Labels[api.LabelIncludedNamespace] = "true"
	}
}

// InjectDecoder injects the decoder.
func (m *Namespace) InjectDecoder(d *admission.Decoder) error {
	m.decoder = d
	return nil
}
