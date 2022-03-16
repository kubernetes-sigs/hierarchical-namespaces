package webhooks

import (
	"fmt"
	"os"

	k8sadm "k8s.io/api/admission/v1"
	authnv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// IsHNCServiceAccount is inspired by isGKServiceAccount from open-policy-agent/gatekeeper.
func IsHNCServiceAccount(user *authnv1.UserInfo) bool {
	if user == nil {
		// useful for unit tests
		return false
	}

	ns, found := os.LookupEnv("POD_NAMESPACE")
	if !found {
		ns = "hnc-system"
	}
	saGroup := fmt.Sprintf("system:serviceaccounts:%s", ns)
	for _, g := range user.Groups {
		if g == saGroup {
			return true
		}
	}
	return false
}

// Allow is a replacement for controller-runtime's admission.Allowed() that allows you to set the
// message (human-readable) as opposed to the reason (machine-readable).
func Allow(msg string) admission.Response {
	return admission.Response{AdmissionResponse: k8sadm.AdmissionResponse{
		Allowed: true,
		Result: &metav1.Status{
			Code:    0,
			Message: msg,
		},
	}}
}

func DenyBadRequest(err error) admission.Response {
	return denyFromAPIError(apierrors.NewBadRequest(err.Error()))
}

func DenyConflict(qualifiedResource schema.GroupResource, name string, err error) admission.Response {
	return denyFromAPIError(apierrors.NewConflict(qualifiedResource, name, err))
}

func DenyForbidden(qualifiedResource schema.GroupResource, name string, err error) admission.Response {
	return denyFromAPIError(apierrors.NewForbidden(qualifiedResource, name, err))
}

func DenyInternalError(err error) admission.Response {
	return denyFromAPIError(apierrors.NewInternalError(err))
}

func DenyInvalid(qualifiedKind schema.GroupKind, name string, errs field.ErrorList) admission.Response {
	err := apierrors.NewInvalid(qualifiedKind, name, errs)
	return denyFromAPIError(err)
}

func DenyServiceUnavailable(reason string) admission.Response {
	return denyFromAPIError(apierrors.NewServiceUnavailable(reason))
}

func DenyUnauthorized(reason string) admission.Response {
	return denyFromAPIError(apierrors.NewUnauthorized(reason))
}

// Deny is a replacement for controller-runtime's admission.Denied() that allows you to set _both_ a
// human-readable message _and_ a machine-readable reason, and also sets the code correctly instead
// of hardcoding it to 403 Forbidden.
//
// NOTE: When manipulating the HNC configuration object via kubectl directly, kubectl
// ignores the Message field and displays the Details field if an error is
// StatusReasonInvalid.
//
// Deprecated: Use one of the explicit deny reason funcs instead; please don't add new callers.
func Deny(reason metav1.StatusReason, msg string) admission.Response {
	err := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Code:    CodeFromReason(reason),
			Message: msg,
			Reason:  reason,
		},
	}
	return denyFromAPIError(err)
}

// denyFromAPIError returns a response for denying a request with provided status error object.
func denyFromAPIError(apiStatus apierrors.APIStatus) admission.Response {
	status := apiStatus.Status()
	return admission.Response{
		AdmissionResponse: k8sadm.AdmissionResponse{
			Allowed: false,
			Result:  &status,
		},
	}
}

// CodeFromReason implements the needed subset of
// https://godoc.org/k8s.io/apimachinery/pkg/apis/meta/v1#StatusReason
func CodeFromReason(reason metav1.StatusReason) int32 {
	switch reason {
	case metav1.StatusReasonUnknown:
		return 500
	case metav1.StatusReasonUnauthorized:
		return 401
	case metav1.StatusReasonForbidden:
		return 403
	case metav1.StatusReasonConflict:
		return 409
	case metav1.StatusReasonBadRequest:
		return 400
	case metav1.StatusReasonInvalid:
		return 422
	case metav1.StatusReasonInternalError:
		return 500
	case metav1.StatusReasonServiceUnavailable:
		return 503
	default:
		return 500
	}
}
