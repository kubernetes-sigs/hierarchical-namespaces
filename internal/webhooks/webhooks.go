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
