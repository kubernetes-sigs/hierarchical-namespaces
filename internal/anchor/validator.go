package anchor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/webhooks"
)

const (
	// ServingPath is where the validator will run. Must be kept in sync
	// with the kubebuilder markers below.
	ServingPath = "/validate-hnc-x-k8s-io-v1alpha2-subnamespaceanchors"
)

// Note: the validating webhook FAILS CLOSE. This means that if the webhook goes down, all further
// changes are forbidden.
//
// +kubebuilder:webhook:admissionReviewVersions=v1,path=/validate-hnc-x-k8s-io-v1alpha2-subnamespaceanchors,mutating=false,failurePolicy=fail,groups="hnc.x-k8s.io",resources=subnamespaceanchors,sideEffects=None,verbs=create;update;delete,versions=v1alpha2,name=subnamespaceanchors.hnc.x-k8s.io

type Validator struct {
	Log     logr.Logger
	Forest  *forest.Forest
	decoder *admission.Decoder
}

// req defines the aspects of the admission.Request that we care about.
type anchorRequest struct {
	anchor *api.SubnamespaceAnchor
	op     k8sadm.Operation
}

// Handle implements the validation webhook.
func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := v.Log.WithValues("ns", req.Namespace, "nm", req.Name, "op", req.Operation, "user", req.UserInfo.Username)
	// Early exit since the HNC SA can do whatever it wants.
	if webhooks.IsHNCServiceAccount(&req.AdmissionRequest.UserInfo) {
		log.V(1).Info("Allowed change by HNC SA")
		return webhooks.Allow("HNC SA")
	}

	decoded, err := v.decodeRequest(log, req)
	if err != nil {
		log.Error(err, "Couldn't decode request")
		return webhooks.DenyBadRequest(err)
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
// tested (ie without constructing a full admission.Request). It validates that the request is allowed
// based on the current in-memory state of the forest.
func (v *Validator) handle(req *anchorRequest) admission.Response {
	v.Forest.Lock()
	defer v.Forest.Unlock()
	pnm := req.anchor.Namespace
	cnm := req.anchor.Name
	cns := v.Forest.Get(cnm)

	errStrs := validation.IsDNS1123Label(cnm)
	if len(errStrs) != 0 {
		fldPath := field.NewPath("metadata", "name")
		msg := fmt.Sprintf("not a valid namespace name: %s", strings.Join(errStrs, "; "))
		allErrs := field.ErrorList{
			field.Invalid(fldPath, cnm, msg),
		}
		return webhooks.DenyInvalid(api.SubnamespaceAnchorGK, cnm, allErrs)
	}

	labelErrs := config.ValidateManagedLabels(req.anchor.Spec.Labels)
	annotationErrs := config.ValidateManagedAnnotations(req.anchor.Spec.Annotations)
	allErrs := append(labelErrs, annotationErrs...)
	if len(allErrs) > 0 {
		return webhooks.DenyInvalid(api.SubnamespaceAnchorGK, req.anchor.Name, allErrs)
	}

	switch req.op {
	case k8sadm.Create:
		// Can't create subnamespaces in unmanaged namespaces
		if why := config.WhyUnmanaged(pnm); why != "" {
			err := fmt.Errorf("cannot create a subnamespace in the unmanaged namespace %q (%s)", pnm, why)
			return webhooks.DenyForbidden(api.SubnamespaceAnchorGR, pnm, err)
		}
		// Can't create subnamespaces using unmanaged namespace names
		if why := config.WhyUnmanaged(cnm); why != "" {
			err := fmt.Errorf("cannot create a subnamespace using the unmanaged namespace name %q (%s)", cnm, why)
			return webhooks.DenyForbidden(api.SubnamespaceAnchorGR, cnm, err)
		}

		// Can't create anchors for existing namespaces, _unless_ it's for a subns with a missing
		// anchor.
		if cns.Exists() {
			childIsMissingAnchor := (cns.Parent().Name() == pnm && cns.IsSub)
			if !childIsMissingAnchor {
				err := errors.New("cannot create a subnamespace using an existing namespace")
				return webhooks.DenyConflict(api.SubnamespaceAnchorGR, cnm, err)
			}
		}

	case k8sadm.Delete:
		// Don't allow the anchor to be deleted if it's in a good state and has descendants of its own,
		// unless allowCascadingDeletion is set.
		if req.anchor.Status.State == api.Ok && cns.ChildNames() != nil && !cns.AllowsCascadingDeletion() {
			err := fmt.Errorf("subnamespace %s is not a leaf and doesn't allow cascading deletion. Please set allowCascadingDeletion flag or make it a leaf first", cnm)
			return webhooks.DenyForbidden(api.SubnamespaceAnchorGR, cnm, err)
		}

	default:
		// nop for updates etc
	}

	return webhooks.Allow("")
}

// decodeRequest gets the information we care about into a simple struct that's easy to both a) use
// and b) factor out in unit tests.
func (v *Validator) decodeRequest(log logr.Logger, in admission.Request) (*anchorRequest, error) {
	anchor := &api.SubnamespaceAnchor{}
	var err error
	// For DELETE request, use DecodeRaw() from req.OldObject, since Decode() only
	// uses req.Object, which will be empty for a DELETE request.
	if in.Operation == k8sadm.Delete {
		log.V(1).Info("Decoding a delete request.")
		err = v.decoder.DecodeRaw(in.OldObject, anchor)
	} else {
		err = v.decoder.Decode(in, anchor)
	}
	if err != nil {
		return nil, err
	}

	return &anchorRequest{
		anchor: anchor,
		op:     in.Operation,
	}, nil
}

func (v *Validator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
