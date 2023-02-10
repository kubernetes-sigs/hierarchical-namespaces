package hrq

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/logutils"
)

const (
	// HRQServingPath is where the validator will run. Must be kept in sync with the
	// kubebuilder markers below.
	HRQServingPath = "/validate-hnc-x-k8s-io-v1alpha2-hrq"

	// validatingResourceQuotaPrefix is the prefix of the name of the resource
	// quota that is created in the dry-run to validate the resource types.
	validatingResourceQuotaPrefix = "hnc-x-k8s-io-hrq-validating-rq-"

	// fieldInfo is the field that has invalid resource types.
	fieldInfo = ".spec.hard"
)

// Note: the validating webhook FAILS CLOSE. This means that if the webhook goes
// down, no further changes are allowed.
//
// +<removewhenready>kubebuilder:webhook:admissionReviewVersions=v1;v1beta1,path=/validate-hnc-x-k8s-io-v1alpha2-hrq,mutating=false,failurePolicy=fail,groups="hnc.x-k8s.io",resources=hierarchicalresourcequotas,sideEffects=None,verbs=create;update,versions=v1alpha2,name=hrq.hnc.x-k8s.io

type HRQ struct {
	server  serverClient
	Log     logr.Logger
	decoder *admission.Decoder
}

// serverClient represents the checks that should typically be performed against
// the apiserver, but need to be stubbed out during unit testing.
type serverClient interface {
	// validate validates if the resource types in the RQ are valid quota types.
	validate(ctx context.Context, inst *v1.ResourceQuota) error
}

func (v *HRQ) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := logutils.WithRID(v.Log).WithValues("nm", req.Name, "nnm", req.Namespace)

	inst := &api.HierarchicalResourceQuota{}
	if err := v.decoder.Decode(req, inst); err != nil {
		log.Error(err, "Couldn't decode request")
		return deny(metav1.StatusReasonBadRequest, err.Error())
	}

	resp := v.handle(ctx, log, inst)
	if resp.Allowed {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	} else {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	}
	return resp
}

// Validate if the resources in the spec are valid resource types for quota. We
// will dry-run writing the HRQ resources to an RQ to see if we get an error
// from the apiserver or not. If yes, we will deny the HRQ request.
func (v *HRQ) handle(ctx context.Context, log logr.Logger, inst *api.HierarchicalResourceQuota) admission.Response {
	rq := &v1.ResourceQuota{}
	rq.Namespace = inst.Namespace
	rq.Name = createRQName()
	rq.Spec.Hard = inst.Spec.Hard
	log.V(1).Info("Validating resource types in the HRQ spec by writing them to a resource quota on apiserver", "limits", inst.Spec.Hard)
	if err := v.server.validate(ctx, rq); err != nil {
		return denyInvalidField(fieldInfo, ignoreRQErr(err.Error()))
	}

	return allow("")
}

func createRQName() string {
	suffix := make([]byte, 10)
	rand.Read(suffix)
	return fmt.Sprintf("%s%x", validatingResourceQuotaPrefix, suffix)
}

// ignoreRQErr ignores the error message on the resource quota object and only
// keeps the field info, e.g. "[spec.hard[roles]: Invalid value ..." instead of
// "ResourceQuota "xxx" is invalid: [spec.hard[roles]: Invalid value ...".
func ignoreRQErr(err string) string {
	return strings.TrimPrefix(err, strings.Split(err, ":")[0]+": ")
}

// realClient implements serverClient, and is not used during unit tests.
type realClient struct {
	client client.Client
}

// validate creates a resource quota on the apiserver if it does not exist.
// Otherwise, it updates existing one on the apiserver. Since our real client is
// a dry-run client, this operation will be a dry-run too.
func (c *realClient) validate(ctx context.Context, inst *v1.ResourceQuota) error {
	if inst.CreationTimestamp.IsZero() {
		if err := c.client.Create(ctx, inst); err != nil {
			return err
		}
		return nil
	}

	if err := c.client.Update(ctx, inst); err != nil {
		return err
	}
	return nil
}

func (v *HRQ) InjectClient(c client.Client) error {
	// Create a dry-run client.
	v.server = &realClient{client: client.NewDryRunClient(c)}
	return nil
}

func (r *HRQ) InjectDecoder(d *admission.Decoder) error {
	r.decoder = d
	return nil
}
