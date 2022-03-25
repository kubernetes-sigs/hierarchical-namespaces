package hierarchyconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/selectors"
	"sigs.k8s.io/hierarchical-namespaces/internal/webhooks"
)

const (
	// ServingPath is where the validator will run. Must be kept in sync with the
	// kubebuilder marker below.
	ServingPath = "/validate-hnc-x-k8s-io-v1alpha2-hierarchyconfigurations"
)

// Note: the validating webhook FAILS CLOSED. This means that if the webhook goes down, all further
// changes to the hierarchy are forbidden. However, new objects will still be propagated according
// to the existing hierarchy (unless the reconciler is down too).
//
// +kubebuilder:webhook:admissionReviewVersions=v1,path=/validate-hnc-x-k8s-io-v1alpha2-hierarchyconfigurations,mutating=false,failurePolicy=fail,groups="hnc.x-k8s.io",resources=hierarchyconfigurations,sideEffects=None,verbs=create;update,versions=v1alpha2,name=hierarchyconfigurations.hnc.x-k8s.io

type Validator struct {
	Log     logr.Logger
	Forest  *forest.Forest
	server  serverClient
	decoder *admission.Decoder
}

// serverClient represents the checks that should typically be performed against the apiserver, but
// need to be stubbed out during unit testing.
type serverClient interface {
	// Exists returns true if the given namespace exists.
	Exists(ctx context.Context, nnm string) (bool, error)

	// IsAdmin takes a UserInfo and the name of a namespace, and returns true if the user is an admin
	// of that namespace (ie, can update the hierarchical config).
	IsAdmin(ctx context.Context, ui *authnv1.UserInfo, nnm string) (bool, error)
}

// request defines the aspects of the admission.Request that we care about.
type request struct {
	hc *api.HierarchyConfiguration
	ui *authnv1.UserInfo
}

// Handle implements the validation webhook.
//
// During updates, the validator currently ignores the existing state of the object (`oldObject`).
// The reason is that most of the checks being performed are on the state of the entire forest, not
// on any one object, so having the _very_ latest information on _one_ object doesn't really help
// us. That is, we're basically forced to assume that the in-memory forest is fully up-to-date.
//
// Obviously, there are times when this assumption will be incorrect - for example, when the HNC is
// just starting up, or perhaps if there have been a lot of changes made very quickly that the
// reconciler has't caught up with yet. In such cases, this validator can produce both false
// negatives (legal changes are incorrectly rejected) or false positives (illegal changes are
// mistakenly allowed).  False negatives can easily be retried and so are not a significant problem,
// since (by definition) we expect the problem to be transient.
//
// False positives are a more serious concern, and fall into two categories: structural failures,
// and authz failures. Regarding structural failures, the reconciler has been designed to assume
// that the validator is _never_ running, and any illegal configuration that makes it into K8s will
// simply be reported via HierarchyConfiguration.Status.Conditions. It's the admins'
// responsibilities to monitor these conditions and ensure that, transient exceptions aside, all
// namespaces are condition-free. Note that even if the validator is working perfectly, it's still
// possible to introduce structural failures, as described in the user docs.
//
// Authz false positives are prevented as described by the comments to `getServerChecks`.
func (v *Validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := v.Log.WithValues("ns", req.Namespace, "op", req.Operation, "user", req.UserInfo.Username)
	decoded, err := v.decodeRequest(req)
	if err != nil {
		log.Error(err, "Couldn't decode request")
		return webhooks.DenyBadRequest(err)
	}

	resp := v.handle(ctx, log, decoded)
	if !resp.Allowed {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	} else {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	}
	return resp
}

// handle implements the non-boilerplate logic of this validator, allowing it to be more easily unit
// tested (ie without constructing a full admission.Request).
//
// This follows the standard HNC pattern of:
// - Load a bunch of stuff from the apiserver
// - Lock the forest and do all checks
// - Finish up with the apiserver (although we just run _additional_ checks, we don't modify things)
//
// This minimizes the amount of time that the forest is locked, allowing different threads to
// proceed in parallel.
func (v *Validator) handle(ctx context.Context, log logr.Logger, req *request) admission.Response {
	// Early exit: the HNC SA can do whatever it wants. This is because if an illegal HC already
	// exists on the K8s server, we need to be able to update its status even though the rest of the
	// object wouldn't pass legality. We should probably only give the HNC SA the ability to modify
	// the _status_, though. TODO: https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/80.
	if isHNCServiceAccount(req.ui) {
		return allow("HNC SA")
	}

	if why := config.WhyUnmanaged(req.hc.Namespace); why != "" {
		err := fmt.Errorf("namespace %q is not managed by HNC (%s) and cannot be set as a child of another namespace", req.hc.Namespace, why)
		// TODO(erikgb): Invalid field error better?
		return webhooks.DenyForbidden(api.HierarchyConfigurationGR, api.Singleton, err)
	}
	if why := config.WhyUnmanaged(req.hc.Spec.Parent); why != "" {
		err := fmt.Errorf("namespace %q is not managed by HNC (%s) and cannot be set as the parent of another namespace", req.hc.Spec.Parent, why)
		// TODO(erikgb): Invalid field error better?
		return webhooks.DenyForbidden(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	labelErrs := config.ValidateManagedLabels(req.hc.Spec.Labels)
	annotationErrs := config.ValidateManagedAnnotations(req.hc.Spec.Annotations)
	allErrs := append(labelErrs, annotationErrs...)
	if len(allErrs) > 0 {
		return webhooks.DenyInvalid(api.HierarchyConfigurationGK, req.hc.Name, allErrs)
	}

	// Do all checks that require holding the in-memory lock. Generate a list of server checks we
	// should perform once the lock is released.
	serverChecks, resp := v.checkForest(req.hc)
	if !resp.Allowed {
		return resp
	}

	// Ensure that the server's in the right state to make the changes.
	return v.checkServer(ctx, log, req.ui, serverChecks)
}

// checkForest validates that the request is allowed based on the current in-memory state of the
// forest. If it is, it returns a list of checks we need to perform against the apiserver in order
// to be allowed to make the change; these checks are executed _after_ the in-memory lock is
// released.
func (v *Validator) checkForest(hc *api.HierarchyConfiguration) ([]serverCheck, admission.Response) {
	v.Forest.Lock()
	defer v.Forest.Unlock()

	// Load stuff from the forest
	ns := v.Forest.Get(hc.ObjectMeta.Namespace)
	curParent := ns.Parent()
	newParent := v.Forest.Get(hc.Spec.Parent)

	// Check problems on the namespace itself
	if resp := v.checkNS(ns); !resp.Allowed {
		return nil, resp
	}

	// Check problems on the parents
	if resp := v.checkParent(ns, curParent, newParent); !resp.Allowed {
		return nil, resp
	}

	// The structure looks good. Get the list of namespaces we need server checks on.
	return v.getServerChecks(curParent, newParent), allow("")
}

// checkNS looks for problems with the current namespace that should prevent changes.
func (v *Validator) checkNS(ns *forest.Namespace) admission.Response {
	// Wait until the namespace has been synced
	if !ns.Exists() {
		msg := fmt.Sprintf("HNC has not reconciled namespace %q yet - please try again in a few moments.", ns.Name())
		return webhooks.DenyServiceUnavailable(msg)
	}

	// Deny the request if the namespace has a halted root - but not if it's halted itself, since we
	// may be trying to resolve the halted condition.
	haltedRoot := ns.GetHaltedRoot()
	if haltedRoot != "" && haltedRoot != ns.Name() {
		err := fmt.Errorf("ancestor %q of namespace %q has a critical condition, which must be resolved before any changes can be made to the hierarchy configuration", haltedRoot, ns.Name())
		// TODO(erikgb): InternalError better?
		return webhooks.DenyForbidden(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	return allow("")
}

// checkParent validates if the parent is legal based on the current in-memory state of the forest.
func (v *Validator) checkParent(ns, curParent, newParent *forest.Namespace) admission.Response {
	if ns.IsExternal() && newParent != nil {
		err := fmt.Errorf("namespace %q is managed by %q, not HNC, so it cannot have a parent in HNC", ns.Name(), ns.Manager)
		// TODO(erikgb): Invalid field error better?
		return webhooks.DenyForbidden(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	if curParent == newParent {
		return allow("parent unchanged")
	}

	// Prevent changing parent of a subnamespace
	if ns.IsSub {
		err := fmt.Errorf("illegal parent: Cannot set the parent of %q to %q because it's a subnamespace of %q", ns.Name(), newParent.Name(), curParent.Name())
		// TODO(erikgb): Invalid field error better?
		return webhooks.DenyConflict(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	// non existence of parent namespace -> not allowed
	if newParent != nil && !newParent.Exists() {
		err := fmt.Errorf("requested parent %q does not exist", newParent.Name())
		// TODO(erikgb): Invalid field error better?
		return webhooks.DenyForbidden(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	// Is this change structurally legal? Note that this can "leak" information about the hierarchy
	// since we haven't done our authz checks yet. However, the fact that they've gotten this far
	// means that the user has permission to update the _current_ namespace, which means they also
	// have visibility into its ancestry and descendents, and this check can only fail if the new
	// parent conflicts with something in the _existing_ hierarchy.
	if reason := ns.CanSetParent(newParent); reason != "" {
		err := fmt.Errorf("illegal parent: %s", reason)
		// TODO(erikgb): Invalid field error better?
		return webhooks.DenyConflict(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	// Prevent overwriting source objects in the descendants after the hierarchy change.
	if co := v.getConflictingObjects(newParent, ns); len(co) != 0 {
		msg := "Cannot update hierarchy because it would overwrite the following object(s):\n"
		msg += "  * " + strings.Join(co, "\n  * ") + "\n"
		msg += "To fix this, please rename or remove the conflicting objects first."
		err := errors.New(msg)
		return webhooks.DenyConflict(api.HierarchyConfigurationGR, api.Singleton, err)
	}

	return allow("")
}

// getConflictingObjects returns a list of namespaced objects if there's any conflict.
func (v *Validator) getConflictingObjects(newParent, ns *forest.Namespace) []string {
	// If the new parent is nil,  early exit since it's impossible to introduce
	// new naming conflicts.
	if newParent == nil {
		return nil
	}
	// Traverse all the types with 'Propagate' mode to find any conflicts.
	conflicts := []string{}
	for _, t := range v.Forest.GetTypeSyncers() {
		if t.GetMode() == api.Propagate {
			conflicts = append(conflicts, v.getConflictingObjectsOfType(t.GetGVK(), newParent, ns)...)
		}
	}
	return conflicts
}

// getConflictingObjectsOfType returns a list of namespaced objects if there's
// any conflict between the new ancestors and the descendants.
func (v *Validator) getConflictingObjectsOfType(gvk schema.GroupVersionKind, newParent, ns *forest.Namespace) []string {
	// Get all the source objects in the new ancestors that would be propagated
	// into the descendants.
	newAnsSrcObjs := make(map[string]bool)
	for _, o := range newParent.GetAncestorSourceObjects(gvk, "") {
		// If the user has chosen not to propagate the object to this descendant,
		// then it should not be included in conflict checks
		if ok, _ := selectors.ShouldPropagate(o, o.GetLabels()); ok {
			newAnsSrcObjs[o.GetName()] = true
		}
	}

	// Look in the descendants to find if there's any conflict.
	cos := []string{}
	dnses := append(ns.DescendantNames(), ns.Name())
	for _, dns := range dnses {
		for _, o := range v.Forest.Get(dns).GetSourceObjects(gvk) {
			if newAnsSrcObjs[o.GetName()] {
				co := fmt.Sprintf("Namespace %q: %s (%v)", dns, o.GetName(), gvk)
				cos = append(cos, co)
			}
		}
	}

	return cos
}

type serverCheckType int

const (
	// checkAuthz verifies that the user is an admin of this namespace
	checkAuthz serverCheckType = iota
	// checkMissing verifies that the namespace does *not* exist on the server
	checkMissing
)

// serverCheck represents a check to perform against the apiserver once the forest lock is released.
type serverCheck struct {
	nnm       string          // the namespace the user needs to be authorized to modify
	checkType serverCheckType // the type of check to perform
	reason    string          // the reason we're checking it (for logs and error messages)
}

// getServerChecks returns the server checks we need to perform in order to verify that this change
// is legal. It must be called while the forest lock is held, but the checks will be performed once
// the lock has been released.
//
// The general rule is that the user must be an admin of the most recent common ancestor of the old
// and new parent, if they're both in the same tree. If they're in *different* trees, the user must
// be an admin of the root of the old tree, and of the new parent. See the user guide or design doc
// for the rationale for this choice.
//
// While this is fairly simple in theory, there are two complications: missing parents and cycles.
//
// If this webhook is working correctly, a namespace can never be deliberately assigned to a parent
// that doesn't exist (in Gitops flows, this means that the client is expected to create all
// namespaces before syncing HierarchyConfiguration objects, or at least be able to keep retrying
// until after all namespaces have been created). Therefore, there are only three cases where an
// ancestor might be missing:
//
// 1. The parent has been deleted, and the namespace is orphaned. In this case, we want to allow the
// namespace to be reassigned to another parent (or become a root) to let admins resolve the problem.
// 2. An ancestor has been deleted, but not the parent. This case is handled by checkNS, above.
// 3. HNC is just starting up and the parent hasn't been synced yet, so we can't determine the tree
// root. In these cases, we want to reject the request and ask the user to try again (e.g. HTTP 503 -
// service unavailable).
//
// Since case #2 is already handled, we only need to distinguish between #1 and #3. So if the
// current parent does not exist in the forest, we ask for a `checkMissing` server check instead of
// the normal `checkAuthz`. If the namespace is _actually_ missing on the apiserver, as expected,
// the check will pass, allowing the admin to fix the error; if it's present (which means we just
// haven't synced it yet), we'll fail with a 503, asking the user to try again later.
//
// The other complication is cycles. We don't do anything special to handle cycles here. If there's
// a cycle, the existing ancestor namespace we select as the "root" will be arbitrary. Hopefully the
// admin trying to resolve the cycle has permissions on *all* the namespaces in the cycle. For the
// new parent, perhaps we should ban moving a namespace *to* a tree with a cycle in it, but that's
// harder to implement and seems like it's not worth the effort.
func (v *Validator) getServerChecks(curParent, newParent *forest.Namespace) []serverCheck {
	// No need for any checks if nothing's changing.
	if curParent == newParent {
		// Note that this also catches the case where both are nil
		return nil
	}

	// If this is currently a root and we're moving into a new tree, just check the parent.
	//
	// Exception: if the current parent is unmanaged (e.g. it used to be managed before, but isn't anymore),
	// treat it as though it's currently a root and allow the change.
	if curParent == nil || !config.IsManagedNamespace(curParent.Name()) {
		if newParent != nil { // could be nil if old parane is unmanaged
			return []serverCheck{{nnm: newParent.Name(), reason: "proposed parent", checkType: checkAuthz}}
		}
		return nil
	}

	// If the current parent is missing, ask for a missing check. Note that only the *direct* parent
	// can be missing; if a more distant ancestor was missing, `ns` would have had a critical
	// condition in the ancestors, and would have failed checkNS.
	if !curParent.Exists() {
		return []serverCheck{{nnm: curParent.Name(), reason: "current missing parent", checkType: checkMissing}}
	}

	// If we're making the namespace into a root, just check the old root.
	curChain := curParent.AncestryNames()
	if newParent == nil {
		return []serverCheck{{nnm: curChain[0], reason: "current root ancestor", checkType: checkAuthz}}
	}

	// This namespace has both old and new parents. If they're in different trees, return the old root
	// and new parent.
	newChain := newParent.AncestryNames()
	if curChain[0] != newChain[0] {
		return []serverCheck{
			{nnm: curChain[0], reason: "current root ancestor", checkType: checkAuthz},
			{nnm: newParent.Name(), reason: "proposed parent", checkType: checkAuthz},
		}
	}

	// There's at least one common ancestor; find the most recent one and return it.
	mrca := curChain[0]
	for i := 1; i < len(curChain) && i < len(newChain); i++ {
		if curChain[i] != newChain[i] {
			break
		}
		mrca = curChain[i]
	}
	return []serverCheck{{
		nnm:       mrca,
		reason:    fmt.Sprintf("most recent common ancestor of current parent %q and proposed parent %q", curParent.Name(), newParent.Name()),
		checkType: checkAuthz,
	}}
}

// checkServer executes the list of requested checks.
func (v *Validator) checkServer(ctx context.Context, log logr.Logger, ui *authnv1.UserInfo, reqs []serverCheck) admission.Response {
	if v.server == nil {
		return allow("") // unit test; TODO put in fake
	}

	// TODO: parallelize?
	for _, req := range reqs {
		switch req.checkType {
		case checkMissing:
			log.Info("Checking existence", "object", req.nnm, "reason", req.reason)
			exists, err := v.server.Exists(ctx, req.nnm)
			if err != nil {
				err = fmt.Errorf("while checking existance for %q, the %s: %w", req.nnm, req.reason, err)
				return webhooks.DenyInternalError(err)
			}

			if exists {
				msg := fmt.Sprintf("HNC has not reconciled namespace %q yet - please try again in a few moments.", req.nnm)
				return webhooks.DenyServiceUnavailable(msg)
			}

		case checkAuthz:
			log.Info("Checking authz", "object", req.nnm, "reason", req.reason)
			allowed, err := v.server.IsAdmin(ctx, ui, req.nnm)
			if err != nil {
				err = fmt.Errorf("while checking authz for %q, the %s: %w", req.nnm, req.reason, err)
				return webhooks.DenyInternalError(err)
			}

			if !allowed {
				return webhooks.DenyUnauthorized(fmt.Sprintf("User %s is not authorized to modify the subtree of %s, which is the %s",
					ui.Username, req.nnm, req.reason))
			}
		}
	}

	return allow("")
}

// decodeRequest gets the information we care about into a simple struct that's easy to both a) use
// and b) factor out in unit tests.
func (v *Validator) decodeRequest(in admission.Request) (*request, error) {
	hc := &api.HierarchyConfiguration{}
	if err := v.decoder.Decode(in, hc); err != nil {
		return nil, err
	}

	return &request{
		hc: hc,
		ui: &in.UserInfo,
	}, nil
}

// isHNCServiceAccount is inspired by isGKServiceAccount from open-policy-agent/gatekeeper.
func isHNCServiceAccount(user *authnv1.UserInfo) bool {
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

func (v *Validator) InjectClient(c client.Client) error {
	v.server = &realClient{client: c}
	return nil
}

func (v *Validator) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}

// realClient implements serverClient, and is not use during unit tests.
type realClient struct {
	client client.Client
}

// Exists implements serverClient
func (r *realClient) Exists(ctx context.Context, nnm string) (bool, error) {
	nsn := types.NamespacedName{Name: nnm}
	ns := &corev1.Namespace{}
	if err := r.client.Get(ctx, nsn, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IsAdmin implements serverClient
func (r *realClient) IsAdmin(ctx context.Context, ui *authnv1.UserInfo, nnm string) (bool, error) {
	// Convert the Extra type
	authzExtra := map[string]authzv1.ExtraValue{}
	for k, v := range ui.Extra {
		authzExtra[k] = (authzv1.ExtraValue)(v)
	}

	// Construct the request
	sar := &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			ResourceAttributes: &authzv1.ResourceAttributes{
				Namespace: nnm,
				Verb:      "update",
				Group:     "hnc.x-k8s.io",
				Version:   "*",
				Resource:  "hierarchyconfigurations",
			},
			User:   ui.Username,
			Groups: ui.Groups,
			UID:    ui.UID,
			Extra:  authzExtra,
		},
	}

	// Call the server
	err := r.client.Create(ctx, sar)

	// Extract the interesting result
	return sar.Status.Allowed, err
}

// allow is a replacement for controller-runtime's admission.Allowed() that allows you to set the
// message (human-readable) as opposed to the reason (machine-readable).
func allow(msg string) admission.Response {
	return admission.Response{AdmissionResponse: k8sadm.AdmissionResponse{
		Allowed: true,
		Result: &metav1.Status{
			Code:    0,
			Message: msg,
		},
	}}
}
