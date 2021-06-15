/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reconcilers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
)

// AnchorReconciler reconciles SubnamespaceAnchor CRs to make sure all the subnamespaces are
// properly maintained.
type AnchorReconciler struct {
	client.Client
	Log logr.Logger

	forest *forest.Forest

	// Affected is a channel of event.GenericEvent (see "Watching Channels" in
	// https://book-v1.book.kubebuilder.io/beyond_basics/controller_watches.html) that is used to
	// enqueue additional objects that need updating.
	Affected chan event.GenericEvent
}

// Reconcile sets up some basic variables and then calls the business logic. It currently
// only handles the creation of the namespaces but no deletion or state reporting yet.
func (r *AnchorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := loggerWithRID(r.Log).WithValues("trigger", req.NamespacedName)
	log.V(1).Info("Reconciling anchor")

	// Get names of the hierarchical namespace and the current namespace.
	nm := req.Name
	pnm := req.Namespace

	// Get instance from apiserver. If the instance doesn't exist, do nothing and early exist because
	// HCR watches anchor and (if applicable) has already updated the parent HC when this anchor was
	// purged.
	inst, err := r.getInstance(ctx, pnm, nm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Anchor has been deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Always delete anchor (and any other HNC CRs) in the excluded namespaces and
	// early exit.
	if config.ExcludedNamespaces[pnm] {
		// Since the anchors in the excluded namespaces are never synced by HNC,
		// there are no finalizers on the anchors that we can delete them without
		// removing the finalizers first.
		log.Info("Deleting anchor in an excluded namespace")
		return ctrl.Result{}, r.deleteInstance(ctx, log, inst)
	}

	// Report "Forbidden" state and early exit if the anchor name is an excluded
	// namespace that should not be created as a subnamespace, but the webhook has
	// been bypassed and the anchor has been successfully created. Forbidden
	// anchors won't have finalizers.
	if config.ExcludedNamespaces[nm] {
		inst.Status.State = api.Forbidden
		return ctrl.Result{}, r.writeInstance(ctx, log, inst)
	}

	// Get the subnamespace. If it doesn't exist, initialize one.
	snsInst, err := r.getNamespace(ctx, nm)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update the state so we know the relationship between the anchor and its namespace.
	r.updateState(log, inst, snsInst)

	// Handle the case where the anchor is being deleted.
	if deleting, err := r.onDeleting(ctx, log, inst, snsInst); deleting {
		return ctrl.Result{}, err
	}

	if err := r.updateNamespace(ctx, log, nm, pnm); err != nil {
		if anchorErr := r.writeInstance(ctx, log, inst); anchorErr != nil {
			log.Error(anchorErr, "while setting anchor state", "state", api.Missing, "reason", err)
		}
		return ctrl.Result{}, err
	}

	// Add finalizers on all non-forbidden anchors to ensure it's not deleted until
	// after the subnamespace is deleted.
	inst.ObjectMeta.Finalizers = []string{api.MetaGroup}
	return ctrl.Result{}, r.writeInstance(ctx, log, inst)
}

// onDeleting returns true if the anchor is in the process of being deleted, and handles all
// necessary steps to allow it to be deleted. This typically amounts to two steps:
//
// 1. Delete the subnamespace controlled by this anchor.
// 2. Remove the finalizers on this anchor, allowing it to be deleted.
//
// There are several conditions where we skip step 1 - for example, if we're uninstalling HNC, or
// if allowCascadingDeletion is disabled but the subnamespace has descendants (see
// shouldDeleteSubns for details). In such cases, we move straight to step 2.
func (r *AnchorReconciler) onDeleting(ctx context.Context, log logr.Logger, inst *api.SubnamespaceAnchor, snsInst *corev1.Namespace) (bool, error) {
	// Early exit and continue reconciliation if the instance is not being deleted.
	if inst.DeletionTimestamp.IsZero() {
		return false, nil
	}

	// We handle deletions differently depending on whether _one_ anchor is being deleted (i.e., the
	// user wants to delete the namespace) or whether the Anchor CRD is being deleted, which usually
	// means HNC is being uninstalled and we shouldn't delete _any_ namespaces.
	deletingCRD, err := isDeletingCRD(ctx, api.Anchors)
	if err != nil {
		log.Error(err, "Couldn't determine if CRD is being deleted")
		return false, err
	}
	log.V(1).Info("Anchor is being deleted", "deletingCRD", deletingCRD)

	// Check if we need to perform step 1 (delete subns), step 2 (allow finalization) or just wait for
	// something to happen. See method-level comments for details.
	switch {
	case r.shouldDeleteSubns(log, inst, snsInst, deletingCRD):
		log.Info("Deleting subnamespace due to anchor being deleted")
		return true, r.deleteNamespace(ctx, log, snsInst)
	case r.shouldFinalizeAnchor(log, inst, snsInst):
		log.V(1).Info("Unblocking deletion") // V(1) since we'll very shortly show an "anchor deleted" message
		inst.ObjectMeta.Finalizers = nil
		return true, r.writeInstance(ctx, log, inst)
	default:
		// There's nothing to do; we're just waiting for something to happen. Print out a log message
		// indicating what we're waiting for.
		if len(inst.ObjectMeta.Finalizers) > 0 {
			log.Info("Waiting for subnamespace to be fully purged before letting the anchor be deleted")
		} else {
			// I doubt we'll ever get here but I suppose it's possible
			log.Info("Waiting for K8s to delete this anchor (all finalizers are removed)")
		}
		return true, nil
	}
}

// shouldDeleteSubns returns true if the namespace still exists and should be deleted as a result of
// the anchor being deleted. There are a variety of reasons why subnamespaces *shouldn't* be deleted
// with their anchors (see the body of this method for details); this function also returns false if
// the subns is already being deleted and we just need to keep waiting for it to disappear.
//
// This is the only part of the deletion process that needs to access the forest, in order to check
// the recursive AllowCascadingDeletion setting.
func (r *AnchorReconciler) shouldDeleteSubns(log logr.Logger, inst *api.SubnamespaceAnchor, nsInst *corev1.Namespace, deletingCRD bool) bool {
	r.forest.Lock()
	defer r.forest.Unlock()

	// If the CRD is being deleted, we want to leave the subnamespace alone.
	if deletingCRD {
		log.Info("Will not delete subnamespace since the anchor CRD is being deleted")
		return false
	}

	// The state of the anchor is the most important factor in whether the namespace should be
	// deleted.
	switch inst.Status.State {
	case api.Ok:
		// The namespace and anchor are bound together, so it's valid to decide the fate of the
		// namespace here. Firstly, if the namespace is already being deleted, or the anchor has already
		// been finalized, it means we've already passed step #1 in the deletion process (see onDeleting
		// for details) so we shouldn't try to perform it again.
		if !nsInst.DeletionTimestamp.IsZero() {
			log.V(1).Info("The subnamespace is already being deleted; no need to delete again")
			return false
		}
		if len(inst.ObjectMeta.Finalizers) == 0 {
			log.V(1).Info("The anchor has already been finalized; do not reconsider deleting the namespace")
			return false
		}

		// The subns is ready to be deleted. It's only safe to delete subnamespaces if they're leaves or
		// if ACD=true on one of their ancestors; the webhooks *should* ensure that this is always true
		// before we get here, but if something slips by, we'll just leave the old subnamespace alone
		// with a missing-anchor condition.
		cns := r.forest.Get(inst.Name)
		if cns.ChildNames() != nil && !cns.AllowsCascadingDeletion() {
			log.Info("This subnamespace has descendants and allowCascadingDeletion is disabled; will not delete")
			return false
		}

		// Everything looks normal and safe. Delete the namespace.
		return true

	case api.Conflict:
		// The anchor is in a bad state; a namespace exists but isn't tied to this anchor. We certainly
		// shouldn't delete the namespace.
		log.Info("Anchor is conflicted; will not delete subnamespace")
		return false

	case api.Missing:
		// This most likely means that the namespace has been deleted. There's a small chance that the
		// user created this anchor and then deleted it before the subns could be created; if so,
		// there's nothing to delete.
		log.V(1).Info("The subnamespace is deleted; no need to delete again")
		return false

	default:
		log.Error(errors.New("Bad anchor state"), "state", inst.Status.State)
		// Stay on the safe side - don't delete
		return false
	}

}

// shouldFinalizeAnchor determines whether the anchor is safe to delete. It should only be called once
// we know that we don't need to delete the subnamespace itself (e.g. it's already gone, it can't be
// deleted, it's in the process of being deleted, etc).
func (r *AnchorReconciler) shouldFinalizeAnchor(log logr.Logger, inst *api.SubnamespaceAnchor, snsInst *corev1.Namespace) bool {
	// If the anchor is already finalized, there's no need to do it again.
	if len(inst.ObjectMeta.Finalizers) == 0 {
		return false
	}

	switch inst.Status.State {
	case api.Ok:
		// The subnamespace exists and is bound to this anchor. Since we called shouldDeleteSubns before
		// this function, we can rely on the subns' deletion timestamp being nonzero if the subns should
		// be deleted with this namespace. If the subns *isn't* being deleted at this point, we can
		// infer that it's not going to be, and the anchor is safe to finalize now.
		if snsInst.DeletionTimestamp.IsZero() {
			log.V(1).Info("Subnamespace will not be deleted; allowing anchor to be finalized")
			return true
		}
		log.V(1).Info("Subnamespace is being deleted; cannot finalize anchor yet")
		return false

	case api.Missing:
		// Most likely, the namespace has been deleted. There's a small chance the anchor was created
		// and then deleted before we were able to create the namespace, in which case, it's also fine
		// to allow the anchor to be deleted.
		//
		// Is it possible that the namespace actually was created, but that HNC just hasn't noticed it
		// yet? I don't _think_ so - we'd create the namespace via the controller-runtime client, and
		// then we'd try to read it via the same client, so I'm hoping there's no weird distributed
		// concurrency problem. But even if this isn't true, the worst that could happen (in this corner
		// case of a corner case) is that the namespace gets created with a Missing Anchor condition,
		// which really isn't the end of the world. So I don't think it's worth worrying about too much.
		log.V(1).Info("Subnamespace does not exist; allowing anchor to be finalized")
		return true

	case api.Conflict:
		// Bad anchors can always be removed.
		log.Info("Anchor is in the Conflict state; allowing it to be deleted")
		return true

	default:
		// Should never happen, so log an error and let it be deleted.
		log.Error(errors.New("Illegal state"), "Unknown state", "state", inst.Status.State)
		return true
	}
}

func (r *AnchorReconciler) updateState(log logr.Logger, inst *api.SubnamespaceAnchor, snsInst *corev1.Namespace) {
	pnm := inst.Namespace
	sOf := snsInst.Annotations[api.SubnamespaceOf]
	switch {
	case snsInst.Name == "":
		log.Info("Subnamespace does not (yet) exist; setting anchor state to Missing")
		inst.Status.State = api.Missing
	case sOf != pnm:
		log.Info("The would-be subnamespace has a conflicting subnamespace-of annotation; setting anchor state to Conflict", "annotation", sOf)
		inst.Status.State = api.Conflict
	default:
		if inst.Status.State != api.Ok {
			// Only print log if something's changed
			log.Info("The subnamespace and its anchor are correctly synchronized", "prevState", inst.Status.State)
		}
		inst.Status.State = api.Ok
	}
}

// It enqueues a subnamespace anchor for later reconciliation. This occurs in a goroutine
// so the caller doesn't block; since the reconciler is never garbage-collected, this is safe.
func (r *AnchorReconciler) enqueue(log logr.Logger, nm, pnm, reason string) {
	go func() {
		// The watch handler doesn't care about anything except the metadata.
		inst := &api.SubnamespaceAnchor{}
		inst.ObjectMeta.Name = nm
		inst.ObjectMeta.Namespace = pnm
		log.V(1).Info("Enqueuing for reconciliation", "affected", pnm+"/"+nm, "reason", reason)
		r.Affected <- event.GenericEvent{Object: inst}
	}()
}

func (r *AnchorReconciler) getInstance(ctx context.Context, pnm, nm string) (*api.SubnamespaceAnchor, error) {
	nsn := types.NamespacedName{Namespace: pnm, Name: nm}
	inst := &api.SubnamespaceAnchor{}
	if err := r.Get(ctx, nsn, inst); err != nil {
		return nil, err
	}
	return inst, nil
}

func (r *AnchorReconciler) writeInstance(ctx context.Context, log logr.Logger, inst *api.SubnamespaceAnchor) error {
	if inst.CreationTimestamp.IsZero() {
		if err := r.Create(ctx, inst); err != nil {
			log.Error(err, "while creating on apiserver")
			return err
		}
	} else {
		if err := r.Update(ctx, inst); err != nil {
			log.Error(err, "while updating on apiserver")
			return err
		}
	}
	return nil
}

// deleteInstance deletes the anchor instance. Note: Make sure there's no
// finalizers on the instance before calling this function.
func (r *AnchorReconciler) deleteInstance(ctx context.Context, log logr.Logger, inst *api.SubnamespaceAnchor) error {
	if err := r.Delete(ctx, inst); err != nil {
		return fmt.Errorf("while deleting on apiserver: %w", err)
	}
	return nil
}

// getNamespace returns the namespace if it exists, or returns an invalid, blank, unnamed one if it
// doesn't. This allows it to be trivially identified as a namespace that doesn't exist, and also
// allows us to easily modify it if we want to create it.
func (r *AnchorReconciler) getNamespace(ctx context.Context, nm string) (*corev1.Namespace, error) {
	ns := &corev1.Namespace{}
	nnm := types.NamespacedName{Name: nm}
	if err := r.Get(ctx, nnm, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		return &corev1.Namespace{}, nil
	}
	return ns, nil
}

func (r *AnchorReconciler) updateNamespace(ctx context.Context, log logr.Logger, nm, pnm string) error {
	pnmInst := &corev1.Namespace{}
	pnnm := types.NamespacedName{Name: pnm}
	if err := r.Get(ctx, pnnm, pnmInst); err != nil {
		return err
	}

	labels := map[string]string{}
	for k, v := range pnmInst.Labels {
		if strings.Contains(k, api.MetaGroup) {
			continue
		}
		labels[k] = v
	}
	inst := corev1apply.Namespace(nm).WithAnnotations(map[string]string{
		api.SubnamespaceOf: pnmInst.Name,
	}).WithLabels(labels)

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(inst)
	if err != nil {
		return err
	}
	patch := &unstructured.Unstructured{
		Object: obj,
	}

	log.Info("Updating subnamespace")
	if err := r.Patch(ctx, patch, client.Apply, &client.PatchOptions{
		FieldManager: api.MetaGroup,
	}); err != nil {
		log.Error(err, "While updating subnamespace")
		return err
	}
	return nil
}

func (r *AnchorReconciler) deleteNamespace(ctx context.Context, log logr.Logger, inst *corev1.Namespace) error {
	if err := r.Delete(ctx, inst); err != nil {
		log.Error(err, "While deleting subnamespace")
		return err
	}
	return nil
}

func (r *AnchorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Maps an subnamespace to its anchor in the parent namespace.
	nsMapFn := func(obj client.Object) []reconcile.Request {
		result := make([]reconcile.Request, 0)
		nss := &corev1.NamespaceList{}
		err := r.List(context.Background(), nss)
		if err != nil {
			return result
		}
		// Fetch subnamespaces
		for _, ns := range nss.Items {
			if obj.GetName() == ns.Name {
				continue
			}
			if _, ok := ns.Labels[obj.GetName()+api.LabelTreeDepthSuffix]; ok {
				result = append(result, reconcile.Request{NamespacedName: types.NamespacedName{
					Name:      ns.Name,
					Namespace: ns.GetAnnotations()[api.SubnamespaceOf],
				}})
			}
		}
		if obj.GetAnnotations()[api.SubnamespaceOf] != "" {
			result = append(result, reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetAnnotations()[api.SubnamespaceOf],
			}})
		}
		return result
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.SubnamespaceAnchor{}).
		Watches(&source.Channel{Source: r.Affected}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Namespace{}}, handler.EnqueueRequestsFromMapFunc(nsMapFn)).
		Complete(r)
}
