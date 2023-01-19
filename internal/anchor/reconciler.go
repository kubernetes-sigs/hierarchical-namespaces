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

package anchor

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/crd"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/logutils"
	"sigs.k8s.io/hierarchical-namespaces/internal/metadata"
)

// Reconciler reconciles SubnamespaceAnchor CRs to make sure all the subnamespaces are
// properly maintained.
type Reconciler struct {
	client.Client
	Log logr.Logger

	Forest *forest.Forest

	// affected is a channel of event.GenericEvent (see "Watching Channels" in
	// https://book-v1.book.kubebuilder.io/beyond_basics/controller_watches.html) that is used to
	// enqueue additional objects that need updating.
	affected chan event.GenericEvent
}

// Reconcile sets up some basic variables and then calls the business logic. It currently
// only handles the creation of the namespaces but no deletion or state reporting yet.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logutils.WithRID(r.Log).WithValues("trigger", req.NamespacedName)
	log.V(1).Info("Reconciling anchor")

	// Get names of the hierarchical namespace and the current namespace.
	nm := req.Name
	pnm := req.Namespace

	// Get instance from apiserver. If the instance doesn't exist, do nothing and early exist because
	// HCR watches anchor and (if applicable) has already updated the parent HC when this anchor was
	// purged.
	baseInst, err := r.getInstance(ctx, pnm, nm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Anchor has been deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	inst := baseInst.DeepCopy()

	// Anchors in unmanaged namespace should be ignored. Make sure it
	// doesn't have any finalizers, otherwise, leave it alone.
	if why := config.WhyUnmanaged(pnm); why != "" {
		if controllerutil.ContainsFinalizer(inst, api.MetaGroup) {
			log.Info("Removing finalizers from anchor in unmanaged namespace", "reason", why)
			controllerutil.RemoveFinalizer(inst, api.MetaGroup)
			return ctrl.Result{}, r.writeInstance(ctx, log, inst, baseInst)
		}
		return ctrl.Result{}, nil
	}

	// Report "Forbidden" state and early exit if the anchor name is an excluded
	// namespace that should not be created as a subnamespace, but the webhook has
	// been bypassed and the anchor has been successfully created. Forbidden
	// anchors won't have finalizers.
	if why := config.WhyUnmanaged(nm); why != "" {
		if inst.Status.State != api.Forbidden || controllerutil.ContainsFinalizer(inst, api.MetaGroup) {
			log.Info("Setting forbidden state on anchor with unmanaged name", "reason", why)
			inst.Status.State = api.Forbidden
			controllerutil.RemoveFinalizer(inst, api.MetaGroup)
			return ctrl.Result{}, r.writeInstance(ctx, log, inst, baseInst)
		}
		return ctrl.Result{}, nil
	}

	// Get the subnamespace. If it doesn't exist, initialize one.
	snsInst, err := r.getNamespace(ctx, nm)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update the state so we know the relationship between the anchor and its namespace.
	r.updateState(log, inst, snsInst)

	// Handle the case where the anchor is being deleted.
	if !inst.DeletionTimestamp.IsZero() {
		// Stop reconciliation as anchor is being deleted
		return ctrl.Result{}, r.onDeleting(ctx, log, inst, baseInst, snsInst)
	}
	// Add finalizers on all non-forbidden anchors to ensure it's not deleted until
	// after the subnamespace is deleted.
	controllerutil.AddFinalizer(inst, api.MetaGroup)

	// If the subnamespace doesn't exist, create it.
	if inst.Status.State == api.Missing {
		if err := r.writeNamespace(ctx, log, nm, pnm); err != nil {
			// Write the "Missing" state to the anchor status if the subnamespace
			// cannot be created for some reason. Without it, the anchor status will
			// remain empty by default.
			if anchorErr := r.writeInstance(ctx, log, inst, baseInst); anchorErr != nil {
				log.Error(anchorErr, "while setting anchor state", "state", api.Missing, "reason", err)
			}
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, r.writeInstance(ctx, log, inst, baseInst)
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
func (r *Reconciler) onDeleting(ctx context.Context, log logr.Logger, inst, baseInst *api.SubnamespaceAnchor, snsInst *corev1.Namespace) error {
	// We handle deletions differently depending on whether _one_ anchor is being deleted (i.e., the
	// user wants to delete the namespace) or whether the Anchor CRD is being deleted, which usually
	// means HNC is being uninstalled and we shouldn't delete _any_ namespaces.
	deletingCRD, err := crd.IsDeletingCRD(ctx, api.Anchors)
	if err != nil {
		log.Error(err, "Couldn't determine if CRD is being deleted")
		return err
	}
	log.V(1).Info("Anchor is being deleted", "deletingCRD", deletingCRD)

	// Check if we need to perform step 1 (delete subns), step 2 (allow finalization) or just wait for
	// something to happen. See method-level comments for details.
	switch {
	case r.shouldDeleteSubns(log, inst, snsInst, deletingCRD):
		log.Info("Deleting subnamespace due to anchor being deleted")
		return r.deleteNamespace(ctx, log, snsInst)
	case r.shouldFinalizeAnchor(log, inst, snsInst):
		log.V(1).Info("Unblocking deletion") // V(1) since we'll very shortly show an "anchor deleted" message
		controllerutil.RemoveFinalizer(inst, api.MetaGroup)
		return r.writeInstance(ctx, log, inst, baseInst)
	default:
		// There's nothing to do; we're just waiting for something to happen. Print out a log message
		// indicating what we're waiting for.
		if controllerutil.ContainsFinalizer(inst, api.MetaGroup) {
			log.Info("Waiting for subnamespace to be fully purged before letting the anchor be deleted")
		} else {
			// I doubt we'll ever get here but I suppose it's possible
			log.Info("Waiting for K8s to delete this anchor (all finalizers are removed)")
		}
		return nil
	}
}

// shouldDeleteSubns returns true if the namespace still exists and should be deleted as a result of
// the anchor being deleted. There are a variety of reasons why subnamespaces *shouldn't* be deleted
// with their anchors (see the body of this method for details); this function also returns false if
// the subns is already being deleted and we just need to keep waiting for it to disappear.
//
// This is the only part of the deletion process that needs to access the forest, in order to check
// the recursive AllowCascadingDeletion setting.
func (r *Reconciler) shouldDeleteSubns(log logr.Logger, inst *api.SubnamespaceAnchor, nsInst *corev1.Namespace, deletingCRD bool) bool {
	r.Forest.Lock()
	defer r.Forest.Unlock()

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
		if !controllerutil.ContainsFinalizer(inst, api.MetaGroup) {
			log.V(1).Info("The anchor has already been finalized; do not reconsider deleting the namespace")
			return false
		}

		// The subns is ready to be deleted. It's only safe to delete subnamespaces if they're leaves or
		// if ACD=true on one of their ancestors; the webhooks *should* ensure that this is always true
		// before we get here, but if something slips by, we'll just leave the old subnamespace alone
		// with a missing-anchor condition.
		cns := r.Forest.Get(inst.Name)
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
		log.Error(errors.New("bad anchor state"), "state", inst.Status.State)
		// Stay on the safe side - don't delete
		return false
	}

}

// shouldFinalizeAnchor determines whether the anchor is safe to delete. It should only be called once
// we know that we don't need to delete the subnamespace itself (e.g. it's already gone, it can't be
// deleted, it's in the process of being deleted, etc).
func (r *Reconciler) shouldFinalizeAnchor(log logr.Logger, inst *api.SubnamespaceAnchor, snsInst *corev1.Namespace) bool {
	// If the anchor is already finalized, there's no need to do it again.
	if !controllerutil.ContainsFinalizer(inst, api.MetaGroup) {
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
		log.Error(errors.New("illegal state"), "Unknown state", "state", inst.Status.State)
		return true
	}
}

func (r *Reconciler) updateState(log logr.Logger, inst *api.SubnamespaceAnchor, snsInst *corev1.Namespace) {
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

// OnChangeNamespace enqueues a subnamespace anchor for later reconciliation. This occurs in a
// goroutine so the caller doesn't block; since the reconciler is never garbage-collected, this is
// safe.
func (r *Reconciler) OnChangeNamespace(log logr.Logger, ns *forest.Namespace) {
	if ns == nil || !ns.IsSub {
		return
	}
	nm := ns.Name()
	pnm := ns.Parent().Name()
	go func() {
		// The watch handler doesn't care about anything except the metadata.
		inst := &api.SubnamespaceAnchor{}
		inst.ObjectMeta.Name = nm
		inst.ObjectMeta.Namespace = pnm
		log.V(1).Info("Enqueuing for reconciliation", "affected", pnm+"/"+nm)
		r.affected <- event.GenericEvent{Object: inst}
	}()
}

func (r *Reconciler) getInstance(ctx context.Context, pnm, nm string) (*api.SubnamespaceAnchor, error) {
	nsn := types.NamespacedName{Namespace: pnm, Name: nm}
	inst := &api.SubnamespaceAnchor{}
	if err := r.Get(ctx, nsn, inst); err != nil {
		return nil, err
	}
	return inst, nil
}

func (r *Reconciler) writeInstance(ctx context.Context, log logr.Logger, inst, baseInst *api.SubnamespaceAnchor) error {
	if inst.CreationTimestamp.IsZero() {
		if err := r.Create(ctx, inst); err != nil {
			log.Error(err, "while creating on apiserver")
			return err
		}
	} else {
		err := r.Patch(ctx, inst, client.MergeFrom(baseInst))
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("Anchor has been deleted")
				return nil
			}
			log.Error(err, "while updating apiserver")
			return err
		}
	}
	return nil
}

// deleteInstance deletes the anchor instance. Note: Make sure there's no
// finalizers on the instance before calling this function.
//
//lint:ignore U1000 Ignore for now, as it may be used again in the future
func (r *Reconciler) deleteInstance(ctx context.Context, inst *api.SubnamespaceAnchor) error {
	if err := r.Delete(ctx, inst); err != nil {
		return fmt.Errorf("while deleting on apiserver: %w", err)
	}
	return nil
}

// getNamespace returns the namespace if it exists, or returns an invalid, blank, unnamed one if it
// doesn't. This allows it to be trivially identified as a namespace that doesn't exist, and also
// allows us to easily modify it if we want to create it.
func (r *Reconciler) getNamespace(ctx context.Context, nm string) (*corev1.Namespace, error) {
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

func (r *Reconciler) writeNamespace(ctx context.Context, log logr.Logger, nm, pnm string) error {
	inst := &corev1.Namespace{}
	inst.ObjectMeta.Name = nm
	metadata.SetAnnotation(inst, api.SubnamespaceOf, pnm)

	// It's safe to use create here since if the namespace is created by someone
	// else while this reconciler is running, returning an error will trigger a
	// retry. The reconciler will set the 'Conflict' state instead of recreating
	// this namespace. All other transient problems should trigger a retry too.
	log.Info("Creating subnamespace")
	if err := r.Create(ctx, inst); err != nil {
		log.Error(err, "While creating subnamespace")
		return err
	}
	return nil
}

func (r *Reconciler) deleteNamespace(ctx context.Context, log logr.Logger, inst *corev1.Namespace) error {
	if err := r.Delete(ctx, inst); err != nil {
		log.Error(err, "While deleting subnamespace")
		return err
	}
	return nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.affected = make(chan event.GenericEvent)

	// Maps an subnamespace to its anchor in the parent namespace.
	nsMapFn := func(obj client.Object) []reconcile.Request {
		if obj.GetAnnotations()[api.SubnamespaceOf] == "" {
			return nil
		}
		return []reconcile.Request{
			{NamespacedName: types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetAnnotations()[api.SubnamespaceOf],
			}},
		}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.SubnamespaceAnchor{}).
		Watches(&source.Channel{Source: r.affected}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Namespace{}}, handler.EnqueueRequestsFromMapFunc(nsMapFn)).
		Complete(r)
}
