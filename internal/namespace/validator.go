package namespaces

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/webhooks"
)

const (
	// ServingPath is where the validator will run. Must be kept in sync
	// with the kubebuilder markers below.
	ServingPath = "/validate-v1-namespace"
)

// Note: the validating webhook FAILS CLOSE. This means that if the webhook goes down, all further
// changes are forbidden.
//
// +kubebuilder:webhook:admissionReviewVersions=v1,path=/validate-v1-namespace,mutating=false,failurePolicy=fail,groups="",resources=namespaces,sideEffects=None,verbs=delete;create;update,versions=v1,name=namespaces.hnc.x-k8s.io

type Validator struct {
	Log     logr.Logger
	Forest  *forest.Forest
	decoder *admission.Decoder
}

// nsRequest defines the aspects of the admission.Request that we care about.
type nsRequest struct {
	ns    *corev1.Namespace
	oldns *corev1.Namespace
	op    k8sadm.Operation
}

// Handle implements the validation webhook.
func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := v.Log.WithValues("nm", req.Name, "op", req.Operation, "user", req.UserInfo.Username)
	// Early exit since the HNC SA can do whatever it wants.
	if webhooks.IsHNCServiceAccount(&req.AdmissionRequest.UserInfo) {
		log.V(1).Info("Allowed change by HNC SA")
		return webhooks.Allow("HNC SA")
	}

	decoded, err := v.decodeRequest(log, req)
	if err != nil {
		log.Error(err, "Couldn't decode request")
		return webhooks.Deny(metav1.StatusReasonBadRequest, err.Error())
	}
	if decoded == nil {
		// https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/688
		return webhooks.Allow("")
	}

	resp := v.handle(decoded)
	if !resp.Allowed {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	} else {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	}
	return resp
}

// handle implements the non-boilerplate logic of this validator, allowing it to be more easily unit
// tested (ie without constructing a full admission.Request).
func (v *Validator) handle(req *nsRequest) admission.Response {
	v.Forest.Lock()
	defer v.Forest.Unlock()

	ns := v.Forest.Get(req.ns.Name)

	switch req.op {
	case k8sadm.Create:
		if rsp := v.illegalIncludedNamespaceLabel(req); !rsp.Allowed {
			return rsp
		}
		// This check only applies to the Create operation since namespace name
		// cannot be updated.
		if rsp := v.nameExistsInExternalHierarchy(req); !rsp.Allowed {
			return rsp
		}

		if rsp := v.illegalTreeLabel(req); !rsp.Allowed {
			return rsp
		}

	case k8sadm.Update:
		if rsp := v.illegalIncludedNamespaceLabel(req); !rsp.Allowed {
			return rsp
		}
		// This check only applies to the Update operation. Creating a namespace
		// with external manager is allowed and we will prevent this conflict by not
		// allowing setting a parent when validating the HierarchyConfiguration.
		if rsp := v.conflictBetweenParentAndExternalManager(req, ns); !rsp.Allowed {
			return rsp
		}

		if rsp := v.illegalTreeLabel(req); !rsp.Allowed {
			return rsp
		}

	case k8sadm.Delete:
		if rsp := v.cannotDeleteSubnamespace(req); !rsp.Allowed {
			return rsp
		}
		if rsp := v.illegalCascadingDeletion(ns); !rsp.Allowed {
			return rsp
		}
	}

	return webhooks.Allow("")
}

// illegalTreeLabel checks if tree labels are being created or modified
// by any user or service account since only HNC service account is
// allowed to do so
func (v *Validator) illegalTreeLabel(req *nsRequest) admission.Response {
	msg := "Cannot set or modify tree label %q in namespace %q; these can only be modified by HNC."

	oldLabels := map[string]string{}
	if req.oldns != nil {
		oldLabels = req.oldns.Labels
	}
	// Ensure the users hasn't added or changed any tree labels
	for key, val := range req.ns.Labels {
		if !strings.Contains(key, api.LabelTreeDepthSuffix) {
			continue
		}

		// Check if new HNC label tree key isn't being added
		if oldLabels[key] != val {
			return webhooks.Deny(metav1.StatusReasonForbidden, fmt.Sprintf(msg, key, req.ns.Name))
		}
	}

	for key := range oldLabels {
		//  Make sure nothing's been deleted
		if strings.Contains(key, api.LabelTreeDepthSuffix) {
			if _, ok := req.ns.Labels[key]; !ok {
				return webhooks.Deny(metav1.StatusReasonForbidden, fmt.Sprintf(msg, key, req.ns.Name))
			}
		}
	}

	return webhooks.Allow("")
}

// illegalIncludedNamespaceLabel checks if there's any illegal use of the
// included-namespace label on namespaces. It only checks a Create or an Update
// request.
func (v *Validator) illegalIncludedNamespaceLabel(req *nsRequest) admission.Response {
	// Early exit if there's no change on the label.
	labelValue, hasLabel := req.ns.Labels[api.LabelIncludedNamespace]
	if req.oldns != nil {
		oldLabelValue, oldHasLabel := req.oldns.Labels[api.LabelIncludedNamespace]
		if oldHasLabel == hasLabel && oldLabelValue == labelValue {
			return webhooks.Allow("")
		}
	}

	isIncluded := config.IsManagedNamespace(req.ns.Name)

	// An excluded namespaces should not have included-namespace label.
	if !isIncluded && hasLabel {
		msg := fmt.Sprintf("You cannot enforce webhook rules on this unmanaged namespace using the %q label. "+
			"See https://github.com/kubernetes-sigs/hierarchical-namespaces/blob/master/docs/user-guide/concepts.md#included-namespace-label "+
			"for detail.", api.LabelIncludedNamespace)
		return webhooks.Deny(metav1.StatusReasonForbidden, msg)
	}

	// An included-namespace should have the included-namespace label with the
	// right value.
	// Note: since we have a mutating webhook to set the correct label if it's
	// missing before this, we only need to check if the label value is correct.
	if isIncluded && labelValue != "true" {
		msg := fmt.Sprintf("You cannot change the value of the %q label. It has to be set as true on all managed namespaces. "+
			"See https://github.com/kubernetes-sigs/hierarchical-namespaces/blob/master/docs/user-guide/concepts.md#included-namespace-label "+
			"for detail.", api.LabelIncludedNamespace)
		return webhooks.Deny(metav1.StatusReasonForbidden, msg)
	}

	return webhooks.Allow("")
}

// nameExistsInExternalHierarchy only applies to the Create operation since
// namespace name cannot be updated.
func (v *Validator) nameExistsInExternalHierarchy(req *nsRequest) admission.Response {
	for _, nm := range v.Forest.GetNamespaceNames() {
		ns := v.Forest.Get(nm)
		if !ns.IsExternal() {
			continue
		}
		externalTreeLabels := ns.GetTreeLabels()
		if _, ok := externalTreeLabels[req.ns.Name]; ok {
			msg := fmt.Sprintf("The namespace name %q is reserved by the external hierarchy manager %q.", req.ns.Name, v.Forest.Get(nm).Manager)
			return webhooks.Deny(metav1.StatusReasonAlreadyExists, msg)
		}
	}
	return webhooks.Allow("")
}

// conflictBetweenParentAndExternalManager only applies to the Update operation.
// Creating a namespace with external manager is allowed and we will prevent
// this conflict by not allowing setting a parent when validating the
// HierarchyConfiguration.
func (v *Validator) conflictBetweenParentAndExternalManager(req *nsRequest, ns *forest.Namespace) admission.Response {
	mgr := req.ns.Annotations[api.AnnotationManagedBy]
	if mgr != "" && mgr != api.MetaGroup && ns.Parent() != nil {
		msg := fmt.Sprintf("Namespace %q is a child of %q. Namespaces with parents defined by HNC cannot also be managed externally. "+
			"To manage this namespace with %q, first make it a root in HNC.", ns.Name(), ns.Parent().Name(), mgr)
		return webhooks.Deny(metav1.StatusReasonForbidden, msg)
	}
	return webhooks.Allow("")
}

// cannotDeleteSubnamespace only applies to the Delete operation.
func (v *Validator) cannotDeleteSubnamespace(req *nsRequest) admission.Response {
	parent := req.ns.Annotations[api.SubnamespaceOf]
	// Early exit if the namespace is not a subnamespace.
	if parent == "" {
		return webhooks.Allow("")
	}

	// If the anchor doesn't exist, we want to allow it to be deleted anyway.
	// See issue https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/847.
	anchorExists := v.Forest.Get(parent).HasAnchor(req.ns.Name)
	if anchorExists {
		msg := fmt.Sprintf("The namespace %s is a subnamespace. Please delete the anchor from the parent namespace %s to delete the subnamespace.", req.ns.Name, parent)
		return webhooks.Deny(metav1.StatusReasonForbidden, msg)
	}
	return webhooks.Allow("")
}

func (v *Validator) illegalCascadingDeletion(ns *forest.Namespace) admission.Response {
	if ns.AllowsCascadingDeletion() {
		return webhooks.Allow("")
	}

	for _, cnm := range ns.ChildNames() {
		if v.Forest.Get(cnm).IsSub {
			msg := "This namespaces contains subnamespaces. Please remove all subnamespaces before deleting this namespace, or set 'allowCascadingDeletion' to delete them automatically."
			return webhooks.Deny(metav1.StatusReasonForbidden, msg)
		}
	}
	return webhooks.Allow("no subnamespaces found")
}

// decodeRequest gets the information we care about into a simple struct that's
// easy to both a) use and b) factor out in unit tests. For Create and Delete,
// the non-empty namespace instance will be put in the `ns` field. Only Update
// request would have a non-empty `oldns` field.
func (v *Validator) decodeRequest(log logr.Logger, in admission.Request) (*nsRequest, error) {
	ns := &corev1.Namespace{}
	oldns := &corev1.Namespace{}
	var err error

	// For DELETE request, use DecodeRaw() from req.OldObject, since Decode() only
	// uses req.Object, which will be empty for a DELETE request.
	if in.Operation == k8sadm.Delete {
		log.V(1).Info("Decoding a delete request.")
		err = v.decoder.DecodeRaw(in.OldObject, ns)
	} else {
		err = v.decoder.Decode(in, ns)
	}
	if err != nil {
		return nil, err
	}

	// Get the old namespace instance from an Update request.
	if in.Operation == k8sadm.Update {
		log.V(1).Info("Decoding an update request.")
		if err = v.decoder.DecodeRaw(in.OldObject, oldns); err != nil {
			return nil, err
		}
	} else {
		oldns = nil
	}

	return &nsRequest{
		ns:    ns,
		oldns: oldns,
		op:    in.Operation,
	}, nil
}

func (v *Validator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
