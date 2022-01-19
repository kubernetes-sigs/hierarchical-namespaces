package hncconfig

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/selectors"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/webhooks"
)

const (
	// ServingPath is where the validator will run. Must be kept in sync
	// with the kubebuilder markers below.
	ServingPath = "/validate-hnc-x-k8s-io-v1alpha2-hncconfigurations"
)

// Note: the validating webhook FAILS CLOSE. This means that if the webhook goes down, all further
// changes are denied.
//
// +kubebuilder:webhook:admissionReviewVersions=v1,path=/validate-hnc-x-k8s-io-v1alpha2-hncconfigurations,mutating=false,failurePolicy=fail,groups="hnc.x-k8s.io",resources=hncconfigurations,sideEffects=None,verbs=create;update;delete,versions=v1alpha2,name=hncconfigurations.hnc.x-k8s.io

type Validator struct {
	Log        logr.Logger
	Forest     *forest.Forest
	translator grTranslator
	decoder    *admission.Decoder
}

// grTranslator checks if a resource exists. The check should typically be
// performed against the apiserver, but need to be stubbed out in unit testing.
type grTranslator interface {
	// gvkForGR returns the mapping GVK for a GR if it exists in the apiserver. If
	// it doesn't exist, return the error.
	gvkForGR(gr schema.GroupResource) (schema.GroupVersionKind, error)
}

type gvkSet map[schema.GroupVersionKind]api.SynchronizationMode

func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := v.Log.WithValues("nm", req.Name, "op", req.Operation, "user", req.UserInfo.Username)
	if webhooks.IsHNCServiceAccount(&req.AdmissionRequest.UserInfo) {
		return webhooks.Allow("HNC SA")
	}

	if req.Operation == k8sadm.Delete {
		if req.Name == api.HNCConfigSingleton {
			return webhooks.Deny(metav1.StatusReasonForbidden, "Deleting the 'config' object is forbidden")
		} else {
			// We allow deleting other objects. We should never enter this case with the CRD validation. We introduced
			// the CRD validation in v0.6. Before that, it was protected by the validation controller. If users somehow
			// bypassed the validation controller and created objects of other names, those objects would still have an
			// obsolete condition and we will allow users to delete the objects.
			return webhooks.Allow("")
		}
	}

	inst := &api.HNCConfiguration{}
	if err := v.decoder.Decode(req, inst); err != nil {
		log.Error(err, "Couldn't decode request")
		return webhooks.Deny(metav1.StatusReasonBadRequest, err.Error())
	}

	resp := v.handle(ctx, inst)
	if !resp.Allowed {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	} else {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	}
	return resp
}

// handle implements the validation logic of this validator for Create and Update operations,
// allowing it to be more easily unit tested (ie without constructing a full admission.Request).
func (v *Validator) handle(ctx context.Context, inst *api.HNCConfiguration) admission.Response {
	ts := gvkSet{}
	// Convert all valid types from GR to GVK. If any type is invalid, e.g. not
	// exist in the apiserver, wrong configuration, deny the request.
	if rp := v.validateTypes(inst, ts); !rp.Allowed {
		return rp
	}

	// Lastly, check if changing a type to "Propagate" mode would cause
	// overwriting user-created objects.
	return v.checkForest(inst, ts)
}

func (v *Validator) validateTypes(inst *api.HNCConfiguration, ts gvkSet) admission.Response {
	for i, r := range inst.Spec.Resources {
		gr := schema.GroupResource{Group: r.Group, Resource: r.Resource}
		field := field.NewPath("spec", "resources").Index(i)
		// Validate the type configured is not an HNC enforced type.
		if !config.EnforcedTypesDisabled && api.IsEnforcedType(r) {
			return webhooks.DenyInvalid(field, fmt.Sprintf("Invalid configuration of %s in the spec, because it's enforced by HNC "+
				"with 'Propagate' mode. Please remove it from the spec.", gr))
		}

		// Validate the type exists in the apiserver. If yes, convert GR to GVK. We
		// use GVK because we will need to checkForest() later to avoid source
		// overwriting conflict (forest uses GVK as the key for object reconcilers).
		gvk, err := v.translator.gvkForGR(gr)
		if err != nil {
			return webhooks.DenyInvalid(field,
				fmt.Sprintf("Cannot find the %s in the apiserver with error: %s", gr, err.Error()))
		}

		// Validate if the configuration of a type already exists. Each type should
		// only have one configuration.
		if _, exists := ts[gvk]; exists {
			return webhooks.DenyInvalid(field, fmt.Sprintf("Duplicate configurations for %s", gr))
		}
		ts[gvk] = r.Mode
	}
	return webhooks.Allow("")
}

func (v *Validator) checkForest(inst *api.HNCConfiguration, ts gvkSet) admission.Response {
	v.Forest.Lock()
	defer v.Forest.Unlock()

	// Get types that are changed from other modes to "Propagate" mode.
	gvks := v.getNewPropagateTypes(ts)

	// Check if user-created objects would be overwritten by these mode changes.
	for gvk := range gvks {
		conflicts := v.checkConflictsForGVK(gvk)
		if len(conflicts) != 0 {
			msg := fmt.Sprintf("Cannot update configuration because setting type %q to 'Propagate' mode would overwrite user-created object(s):\n", gvk)
			msg += strings.Join(conflicts, "\n")
			msg += "\nTo fix this, please rename or remove the conflicting objects first."
			return webhooks.Deny(metav1.StatusReasonConflict, msg)
		}
	}

	return webhooks.Allow("")
}

// checkConflictsForGVK looks for conflicts from top down for each tree.
func (v *Validator) checkConflictsForGVK(gvk schema.GroupVersionKind) []string {
	conflicts := []string{}
	for _, ns := range v.Forest.GetRoots() {
		conflicts = append(conflicts, v.checkConflictsForTree(gvk, ancestorObjects{}, ns)...)
	}
	return conflicts
}

// checkConflictsForTree check for all the gvk objects in the given namespaces, to see if they
// will be potentially overwritten by the objects on the ancestor namespaces
func (v *Validator) checkConflictsForTree(gvk schema.GroupVersionKind, ao ancestorObjects, ns *forest.Namespace) []string {
	conflicts := []string{}
	// make a local copy of the ancestorObjects so that the original copy doesn't get modified
	objs := ao.copy()
	for _, o := range ns.GetSourceObjects(gvk) {
		onm := o.GetName()
		// If there exists objects with the same name and gvk, check if there will be overwriting conflict
		for _, nnm := range objs[onm] {
			// check if the existing ns will propagate this object to the current ns
			inst := v.Forest.Get(nnm).GetSourceObject(gvk, onm)
			if ok, _ := selectors.ShouldPropagate(inst, ns.GetLabels()); ok {
				conflicts = append(conflicts, fmt.Sprintf("  Object %q in namespace %q would overwrite the one in %q", onm, nnm, ns.Name()))
			}
		}
		objs.add(onm, ns)
	}
	// This is cycle-free and safe because we only start the
	// "checkConflictsForTree" from roots in the forest with cycles omitted and
	// it's impossible to get cycles from non-root.
	for _, cnm := range ns.ChildNames() {
		cns := v.Forest.Get(cnm)
		conflicts = append(conflicts, v.checkConflictsForTree(gvk, objs, cns)...)
	}
	return conflicts
}

// getNewPropagateTypes returns a set of types that are changed from other modes
// to `Propagate` mode.
func (v *Validator) getNewPropagateTypes(ts gvkSet) gvkSet {
	// Get all "Propagate" mode types in the new configuration.
	newPts := gvkSet{}
	for gvk, mode := range ts {
		if mode == api.Propagate {
			newPts[gvk] = api.Propagate
		}
	}

	// Remove all existing "Propagate" mode types in the forest (current configuration).
	for _, t := range v.Forest.GetTypeSyncers() {
		_, exist := newPts[t.GetGVK()]
		if t.GetMode() == api.Propagate && exist {
			delete(newPts, t.GetGVK())
		}
	}

	return newPts
}

// ancestorObjects maps an object name to the ancestor namespace(s) in which
// it's defined.
type ancestorObjects map[string][]string

func (a ancestorObjects) copy() ancestorObjects {
	copy := ancestorObjects{}
	for k, v := range a {
		copy[k] = v
	}
	return copy
}

func (a ancestorObjects) add(onm string, ns *forest.Namespace) {
	a[onm] = append(a[onm], ns.Name())
}

// realGRTranslator implements grTranslator, and is not used during unit tests.
type realGRTranslator struct {
	config *rest.Config
}

// gvkForGR searches a given GR in the apiserver and returns the mapping GVK. It
// returns an empty GVK and an error if the GR doesn't exist.
func (r *realGRTranslator) gvkForGR(gr schema.GroupResource) (schema.GroupVersionKind, error) {
	allRes, err := getAPIGroupResources(r.config)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}

	return gvkForGR(gr, allRes)
}

func (v *Validator) InjectConfig(cf *rest.Config) error {
	v.translator = &realGRTranslator{config: cf}
	return nil
}

func (v *Validator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
