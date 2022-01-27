package objects

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/metadata"
	"sigs.k8s.io/hierarchical-namespaces/internal/selectors"
	"sigs.k8s.io/hierarchical-namespaces/internal/webhooks"
)

const (
	// ServingPath is where the validator will run. Must be kept in sync
	// with the kubebuilder markers below.
	ServingPath = "/validate-objects"
)

// Note: the validating webhook FAILS CLOSE. This means that if the webhook goes
// down, all further changes are forbidden. In addition, the webhook `rules`
// (groups, resources, versions, verbs) specified in the below kubebuilder marker
// are overwritten by the `rules` configured in config/webhook/webhook_patch.yaml,
// because there's no marker for `scope` and we only want this object webhook
// to work on `namespaced` objects. Please make sure you edit the webhook_patch.yaml
// file if you want to change the webhook `rules` and better make the rules
// here the same as what's in the webhook_patch.yaml.
//
// +kubebuilder:webhook:admissionReviewVersions=v1,path=/validate-objects,mutating=false,failurePolicy=fail,groups="*",resources="*",sideEffects=None,verbs=create;update;delete,versions="*",name=objects.hnc.x-k8s.io

type Validator struct {
	Log     logr.Logger
	Forest  *forest.Forest
	client  client.Client
	decoder *admission.Decoder
}

type request struct {
	obj    *unstructured.Unstructured
	oldObj *unstructured.Unstructured
	op     k8sadm.Operation
}

func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := v.Log.WithValues("resource", req.Resource, "ns", req.Namespace, "nm", req.Name, "op", req.Operation, "user", req.UserInfo.Username)

	// Before even looking at the objects, early-exit for any changes we shouldn't be involved in.
	// This reduces the chance we'll hose some aspect of the cluster we weren't supposed to touch.
	//
	// Firstly, skip namespaces we're excluded from (like kube-system).
	// Note: This is added just in case the "hnc.x-k8s.io/excluded-namespace=true"
	// label is not added on the excluded namespaces. VWHConfiguration of this VWH
	// already has a `namespaceSelector` to exclude namespaces with the label.
	if !config.IsManagedNamespace(req.Namespace) {
		return webhooks.Allow("unmanaged namespace " + req.Namespace)
	}
	// Allow changes to the types that are not in propagate mode. This is to dynamically enable/disable
	// object webhooks based on the types configured in hncconfig. Since the current admission rules only
	// apply to propagated objects, we can disable object webhooks on all other non-propagate-mode types.
	if !v.isPropagateType(req.Kind) {
		return webhooks.Allow("Non-propagate-mode types")
	}
	// Finally, let the HNC SA do whatever it wants.
	if webhooks.IsHNCServiceAccount(&req.AdmissionRequest.UserInfo) {
		log.V(1).Info("Allowed change by HNC SA")
		return webhooks.Allow("HNC SA")
	}

	decoded, err := v.decodeRequest(log, req)
	if err != nil {
		return webhooks.DenyBadRequest(err)
	}

	// Run the actual logic.
	resp := v.handle(ctx, decoded)
	if !resp.Allowed {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	} else {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	}
	return resp
}

func (v *Validator) isPropagateType(gvk metav1.GroupVersionKind) bool {
	v.Forest.Lock()
	defer v.Forest.Unlock()

	ts := v.Forest.GetTypeSyncerFromGroupKind(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind})
	return ts != nil && ts.GetMode() == api.Propagate
}

// handle implements the non-webhook-y businesss logic of this validator, allowing it to be more
// easily unit tested (ie without constructing an admission.Request, setting up user infos, etc).
func (v *Validator) handle(ctx context.Context, req *request) admission.Response {
	inst := req.obj
	oldInst := req.oldObj

	// Find out if the object was/is inherited, and where it's inherited from.
	oldSource, oldInherited := metadata.GetLabel(oldInst, api.LabelInheritedFrom)
	newSource, newInherited := metadata.GetLabel(inst, api.LabelInheritedFrom)

	// If the object wasn't and isn't inherited, we will check to see if the
	// source can be created without causing any conflict.
	if !oldInherited && !newInherited {
		// check if there is any invalid HNC annotation
		if msg := validateSelectorAnnot(inst); msg != "" {
			return webhooks.DenyBadRequest(errors.New(msg))
		}
		// check selector format
		// If this is a selector change, and the new selector is not valid, we'll deny this operation
		if err := validateSelectorChange(inst, oldInst); err != nil {
			return webhooks.DenyBadRequest(fmt.Errorf("invalid Kubernetes labelSelector: %w", err))
		}
		if err := validateTreeSelectorChange(inst, oldInst); err != nil {
			return webhooks.DenyBadRequest(fmt.Errorf("invalid HNC %q value: %w", api.AnnotationTreeSelector, err))
		}
		if err := validateNoneSelectorChange(inst, oldInst); err != nil {
			return webhooks.DenyBadRequest(err)
		}
		if msg := validateSelectorUniqueness(inst, oldInst); msg != "" {
			return webhooks.DenyBadRequest(errors.New(msg))
		}

		if yes, dnses := v.hasConflict(inst); yes {
			dnsesStr := strings.Join(dnses, "\n  * ")
			msg := fmt.Sprintf("\nCannot create %q (%s) in namespace %q because it would overwrite objects in the following descendant namespace(s):\n  * %s\nTo fix this, choose a different name for the object, or remove the conflicting objects from the above namespaces.", inst.GetName(), inst.GroupVersionKind(), inst.GetNamespace(), dnsesStr)
			return webhooks.Deny(metav1.StatusReasonConflict, msg)
		}
		return webhooks.Allow("source object")
	}
	// This is a propagated object.
	return v.handleInherited(ctx, req, newSource, oldSource)
}

func validateSelectorAnnot(inst *unstructured.Unstructured) string {
	annots := inst.GetAnnotations()
	for key := range annots {
		// for example: segs = ["propagate.hnc.x-k8s.io", "select"]
		segs := strings.SplitN(key, "/", 2)
		// for example: prefix = ["propagate", "hnc.x-k8s.io"]
		prefix := strings.SplitN(segs[0], ".", 2)
		if len(prefix) < 2 || prefix[1] != api.MetaGroup {
			continue
		}
		msg := "invalid HNC exceptions annotation: %v, should be one of the following: " +
			api.AnnotationSelector + "; " + api.AnnotationTreeSelector + "; " +
			api.AnnotationNoneSelector
		// If this annotation is part of HNC metagroup, we check if the prefix value is valid
		if segs[0] != api.AnnotationPropagatePrefix {
			return fmt.Sprintf(msg, key)
		}
		// check if the suffix is valid by checking the whole annotation key
		if key != api.AnnotationSelector &&
			key != api.AnnotationTreeSelector &&
			key != api.AnnotationNoneSelector {
			return fmt.Sprintf(msg, key)
		}
	}
	return ""
}

func validateSelectorUniqueness(inst, oldInst *unstructured.Unstructured) string {
	sel := selectors.GetSelectorAnnotation(inst)
	treeSel := selectors.GetTreeSelectorAnnotation(inst)
	noneSel := selectors.GetNoneSelectorAnnotation(inst)

	oldSel := selectors.GetSelectorAnnotation(oldInst)
	oldTreeSel := selectors.GetTreeSelectorAnnotation(oldInst)
	oldNoneSel := selectors.GetNoneSelectorAnnotation(oldInst)

	isSelectorChange := oldSel != sel || oldTreeSel != treeSel || oldNoneSel != noneSel
	if !isSelectorChange {
		return ""
	}
	found := []string{}
	if sel != "" {
		found = append(found, api.AnnotationSelector)
	}
	if treeSel != "" {
		found = append(found, api.AnnotationTreeSelector)
	}
	if noneSel != "" {
		found = append(found, api.AnnotationNoneSelector)
	}
	if len(found) <= 1 {
		return ""
	}
	msg := "cannot have more than one selector at the same time, but got multiple: %v"
	return fmt.Sprintf(msg, strings.Join(found, ", "))
}

func validateSelectorChange(inst, oldInst *unstructured.Unstructured) error {
	oldSelectorStr := selectors.GetSelectorAnnotation(oldInst)
	newSelectorStr := selectors.GetSelectorAnnotation(inst)
	if newSelectorStr == "" || oldSelectorStr == newSelectorStr {
		return nil
	}
	_, err := selectors.GetSelector(inst)
	return err
}

func validateTreeSelectorChange(inst, oldInst *unstructured.Unstructured) error {
	oldSelectorStr := selectors.GetTreeSelectorAnnotation(oldInst)
	newSelectorStr := selectors.GetTreeSelectorAnnotation(inst)
	if newSelectorStr == "" || oldSelectorStr == newSelectorStr {
		return nil
	}
	_, err := selectors.GetTreeSelector(inst)
	return err
}

func validateNoneSelectorChange(inst, oldInst *unstructured.Unstructured) error {
	oldSelectorStr := selectors.GetNoneSelectorAnnotation(oldInst)
	newSelectorStr := selectors.GetNoneSelectorAnnotation(inst)
	if newSelectorStr == "" || oldSelectorStr == newSelectorStr {
		return nil
	}
	_, err := selectors.GetNoneSelector(inst)
	return err
}

func (v *Validator) handleInherited(ctx context.Context, req *request, newSource, oldSource string) admission.Response {
	op := req.op
	inst := req.obj
	oldInst := req.oldObj

	// Propagated objects cannot be created or deleted (except by the HNC SA, but the HNC SA
	// never gets this far in the validation). They *can* have their statuses updated, so
	// if this is an update, make sure that the canonical form of the object hasn't changed.
	switch op {
	case k8sadm.Create:
		return webhooks.Deny(metav1.StatusReasonForbidden, "Cannot create objects with the label \""+api.LabelInheritedFrom+"\"")

	case k8sadm.Delete:
		// There are few things more irritating in (K8s) life than having some stupid controller stop
		// your namespace from being deleted. If there's an object in here and we've decided that the
		// namespace should be deleted, then don't block anything!
		isDeleting, err := v.isDeletingNS(ctx, oldInst.GetNamespace())
		if err != nil {
			return webhooks.DenyInternalError(fmt.Errorf("cannot delete object propagated from namespace %s with error: %w", oldSource, err))
		}

		if !isDeleting {
			return webhooks.Deny(metav1.StatusReasonForbidden, "Cannot delete object propagated from namespace \""+oldSource+"\"")
		}

		return webhooks.Allow("allowing deletion of propagated object since namespace is being deleted")

	case k8sadm.Update:
		// If the values have changed, that's an illegal modification. This includes if the label is
		// added or deleted. Note that this label is *not* included in canonical(), below, so we
		// need to check it manually.
		if newSource != oldSource {
			return webhooks.Deny(metav1.StatusReasonForbidden, "Cannot modify the label \""+api.LabelInheritedFrom+"\"")
		}

		// If the existing object has an inheritedFrom label, it's a propagated object. Any user changes
		// should be rejected. Note that canonical does *not* compare any HNC labels or
		// annotations.
		if !reflect.DeepEqual(canonical(inst), canonical(oldInst)) {
			return webhooks.Deny(metav1.StatusReasonForbidden,
				"Cannot modify object propagated from namespace \""+oldSource+"\"")
		}

		// Check for all the labels and annotations (including HNC and non HNC)
		if !reflect.DeepEqual(oldInst.GetLabels(), inst.GetLabels()) || !reflect.DeepEqual(oldInst.GetAnnotations(), inst.GetAnnotations()) {
			return webhooks.Deny(metav1.StatusReasonForbidden,
				"Cannot modify object propagated from namespace \""+oldSource+"\"")
		}

		return webhooks.Allow("no illegal updates to propagated object")
	}

	// If you get here, it means the webhook config is misconfigured to include an operation that we
	// actually don't support.
	return webhooks.DenyInternalError(fmt.Errorf("unknown operation: %s", op))
}

// validateDeletingNS validates if the namespace of the object is already being deleted
func (v *Validator) isDeletingNS(ctx context.Context, ns string) (bool, error) {
	nsObj := &corev1.Namespace{}
	nnm := types.NamespacedName{Name: ns}
	if err := v.client.Get(ctx, nnm, nsObj); err != nil {
		// `IsNotFound` should never happen, but if for some bizarre reason the namespace appears to be deleted before the object,
		// we should allow the object to be deleted too.
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("while determining whether namespace %q is being deleted: %w", ns, err)
	}

	if !nsObj.DeletionTimestamp.IsZero() {
		return true, nil
	}

	return false, nil
}

// hasConflict checks if there's any conflicting objects in the descendants. Returns
// true and a list of conflicting descendants, if yes.
func (v *Validator) hasConflict(inst *unstructured.Unstructured) (bool, []string) {
	v.Forest.Lock()
	defer v.Forest.Unlock()

	// If the instance is empty (for a delete operation) or it's not namespace-scoped,
	// there must be no conflict.
	if inst == nil || inst.GetNamespace() == "" {
		return false, nil
	}

	nm := inst.GetName()
	gvk := inst.GroupVersionKind()
	descs := v.Forest.Get(inst.GetNamespace()).DescendantNames()
	conflicts := []string{}

	// Get a list of conflicting descendants if there's any.
	for _, desc := range descs {
		if v.Forest.Get(desc).HasSourceObject(gvk, nm) {
			// If the user have chosen not to propagate the object to this descendant,
			// there shouldn't be any conflict reported here
			nsLabels := v.Forest.Get(inst.GetNamespace()).GetLabels()
			if ok, _ := selectors.ShouldPropagate(inst, nsLabels); ok {
				conflicts = append(conflicts, desc)
			}
		}
	}

	return len(conflicts) != 0, conflicts
}

func (v *Validator) decodeRequest(log logr.Logger, req admission.Request) (*request, error) {
	// Decode the old and new object, if we expect them to exist ("old" won't exist for creations,
	// while "new" won't exist for deletions).
	inst := &unstructured.Unstructured{}
	oldInst := &unstructured.Unstructured{}
	if req.Operation != k8sadm.Delete {
		if err := v.decoder.Decode(req, inst); err != nil {
			log.Error(err, "Couldn't decode req.Object", "raw", req.Object)
			return nil, fmt.Errorf("while decoding object: %w", err)
		}
	}
	if req.Operation != k8sadm.Create {
		if err := v.decoder.DecodeRaw(req.OldObject, oldInst); err != nil {
			log.Error(err, "Couldn't decode req.OldObject", "raw", req.OldObject)
			return nil, fmt.Errorf("while decoding old object: %w", err)
		}
	}

	return &request{
		obj:    inst,
		oldObj: oldInst,
		op:     req.Operation,
	}, nil
}

func (v *Validator) InjectClient(c client.Client) error {
	v.client = c
	return nil
}

func (v *Validator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
