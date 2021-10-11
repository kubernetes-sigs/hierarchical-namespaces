package webhooks

import (
	"fmt"
	"os"

	k8sadm "k8s.io/api/admission/v1"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// Deny is a replacement for controller-runtime's admission.Denied() that allows you to set _both_ a
// human-readable message _and_ a machine-readable reason, and also sets the code correctly instead
// of hardcoding it to 403 Forbidden.
func Deny(reason metav1.StatusReason, msg string) admission.Response {
	return admission.Response{AdmissionResponse: k8sadm.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Code:    CodeFromReason(reason),
			Message: msg,
			Reason:  reason,
		}},
	}
}

// DenyInvalid is a wrapper for Deny with reason metav1.StatusReasonInvalid
func DenyInvalid(field *field.Path, msg string) admission.Response {
	// We need to set the custom message in both Details and Message fields.
	//
	// When manipulating the HNC configuration object via kubectl directly, kubectl
	// ignores the Message field and displays the Details field if an error is
	// StatusReasonInvalid (see implementation here: https://github.com/kubernetes/kubectl/blob/master/pkg/cmd/util/helpers.go#L145-L160).
	//
	// When manipulating the HNC configuration object via the hns kubectl plugin,
	// if an error is StatusReasonInvalid, only the Message field will be displayed. This is because
	// the Error method (https://github.com/kubernetes/client-go/blob/cb664d40f84c27bee45c193e4acb0fcd549b0305/rest/request.go#L1273)
	// calls FromObject (https://github.com/kubernetes/apimachinery/blob/7e441e0f246a2db6cf1855e4110892d1623a80cf/pkg/api/errors/errors.go#L100),
	// which generates a StatusError (https://github.com/kubernetes/apimachinery/blob/7e441e0f246a2db6cf1855e4110892d1623a80cf/pkg/api/errors/errors.go#L35) object.
	// *StatusError implements the Error interface using only the Message
	// field (https://github.com/kubernetes/apimachinery/blob/7e441e0f246a2db6cf1855e4110892d1623a80cf/pkg/api/errors/errors.go#L49)).
	// Therefore, when displaying the error, only the Message field will be available.
	resp := Deny(metav1.StatusReasonInvalid, msg)
	resp.Result.Details = &metav1.StatusDetails{
		Causes: []metav1.StatusCause{{
			Message: msg,
			Field:   field.String(),
		}},
	}

	return resp
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
