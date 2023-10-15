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

package objects

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/logutils"
	"sigs.k8s.io/hierarchical-namespaces/internal/metadata"
	"sigs.k8s.io/hierarchical-namespaces/internal/selectors"
	"sigs.k8s.io/hierarchical-namespaces/internal/stats"
)

// syncAction is the action to take after Reconcile syncs with the in-memory forest.
// This is introduced to consolidate calls with forest lock.
type syncAction string

const (
	actionUnknown syncAction = "<hnc internal error - unknown action>"
	actionRemove  syncAction = "remove"
	actionWrite   syncAction = "write"
	actionNop     syncAction = "no-op"

	unknownSourceNamespace = "<unknown-source-namespace>"
)

// namespacedNameSet is used to keep track of existing propagated objects of
// a specific GVK in the cluster.
type namespacedNameSet map[types.NamespacedName]bool

// Reconciler reconciles generic propagated objects. You must create one for each
// group/version/kind that needs to be propagated and set its `GVK` field appropriately.
type Reconciler struct {
	client.Client
	EventRecorder record.EventRecorder
	Log           logr.Logger

	// Forest is the in-memory forest managed by the HierarchyConfigReconciler.
	Forest *forest.Forest

	// GVK is the group/version/kind handled by this reconciler.
	GVK schema.GroupVersionKind

	// Mode describes propagation mode of objects that are handled by this reconciler.
	// See more details in the comments of api.SynchronizationMode.
	Mode api.SynchronizationMode

	// Affected is a channel of event.GenericEvent (see "Watching Channels" in
	// https://book-v1.book.kubebuilder.io/beyond_basics/controller_watches.html) that is used to
	// enqueue additional objects that need updating.
	Affected chan event.GenericEvent

	// propagatedObjectsLock is used to prevent the race condition between concurrent reconciliation threads
	// trying to update propagatedObjects at the same time.
	propagatedObjectsLock sync.Mutex

	// propagatedObjects contains all propagated objects of the GVK handled by this reconciler.
	propagatedObjects namespacedNameSet
}

type HNCConfigReconcilerType interface {
	Enqueue(reason string)
}

// HNC doesn't actually need all these permissions, but we *do* need to have them to be able to
// propagate RoleBindings for them. These match the permissions required by the builtin
// `cluster-admin` role, as seen in issues #772 and #1311.
//
// +kubebuilder:rbac:groups=*,resources=*,verbs=*

// OnChangeNamespace is called whenever a namespace is changed (according to
// HierarchyConfigReconciler). It enqueues all the current objects in the namespace and local copies
// of the original objects in the ancestors.
func (r *Reconciler) OnChangeNamespace(log logr.Logger, ns *forest.Namespace) {
	log = log.WithValues("gvk", r.GVK)
	// Copy all information from the forest, as the lock will be released before the goroutines
	// finish.
	nsnm := ns.Name()
	srcnms := ns.Parent().GetAncestorSourceNames(r.GVK, "")

	// Enqueue all the current objects in the namespace because some of them may have been deleted.
	go func() {
		// Usually, if we hit an error, we just return it and let controller-runtime worry about it. But
		// since this is in a goroutine, we can't return it. Try for a minute and then give up - this
		// will generate a lot of errors in the log if there's a problem.
		//
		// TODO(#182): look into fixing this holistically
		attempts := 0
		for {
			attempts++
			err := r.enqueueExistingObjects(context.Background(), log, nsnm)
			if err == nil {
				break
			}
			if attempts == 6 {
				log.Error(err, "Couldn't list objects for 60s, giving up. OBJECTS MAY BE IN A BAD STATE.")
				break
			}
			log.Error(err, "Couldn't list objects")
			time.Sleep(10 * time.Second)
		}
	}()

	// Enqueue local copies of the originals in the ancestors to catch any new or changed objects.
	go func() {
		for _, nnm := range srcnms {
			inst := &unstructured.Unstructured{}
			inst.SetGroupVersionKind(r.GVK)
			inst.SetName(nnm.Name)
			inst.SetNamespace(nnm.Namespace)
			log.V(1).Info("Enqueuing local copy of the ancestor original for reconciliation", "affected", inst.GetName())
			r.Affected <- event.GenericEvent{Object: inst}
		}
	}()
}

// GetGVK provides GVK that is handled by this reconciler.
func (r *Reconciler) GetGVK() schema.GroupVersionKind {
	return r.GVK
}

// GetMode provides the mode of objects that are handled by this reconciler.
func (r *Reconciler) GetMode() api.SynchronizationMode {
	return r.Mode
}

// GetValidateMode returns a valid api.SynchronizationMode based on the given mode. Please
// see the comments of api.SynchronizationMode for currently supported modes.
// If mode is not set, it will be api.Propagate by default. Any unrecognized mode is
// treated as api.Ignore.
func GetValidateMode(mode api.SynchronizationMode, log logr.Logger) api.SynchronizationMode {
	switch mode {
	case api.Propagate, api.Ignore, api.Remove, api.AllowPropagate:
		return mode
	case "":
		log.Info("Sync mode is unset; using default 'Propagate'")
		return api.Propagate
	default:
		log.Info("Unrecognized sync mode; will interpret as 'Ignore'", "mode", mode)
		return api.Ignore
	}
}

// SetMode sets the Mode field of an object reconciler and syncs objects in the cluster if needed.
// The method will return an error if syncs fail.
func (r *Reconciler) SetMode(ctx context.Context, log logr.Logger, mode api.SynchronizationMode) error {
	log = log.WithValues("gvk", r.GVK)
	newMode := GetValidateMode(mode, log)
	oldMode := r.Mode
	if newMode == oldMode {
		return nil
	}
	log.Info("Changing sync mode of the object reconciler", "oldMode", oldMode, "newMode", newMode)
	r.Mode = newMode
	// If the new mode is not "ignore", we need to update objects in the cluster
	// (e.g., propagate or remove existing objects).
	if newMode != api.Ignore {
		err := r.enqueueAllObjects(ctx, r.Log)
		if err != nil {
			return err
		}
	}
	return nil
}

// CanPropagate returns true if Propagate mode or AllowPropagate mode is set
func (r *Reconciler) CanPropagate() bool {
	return (r.GetMode() == api.Propagate || r.GetMode() == api.AllowPropagate)
}

// GetNumPropagatedObjects returns the number of propagated objects of the GVK handled by this object reconciler.
func (r *Reconciler) GetNumPropagatedObjects() int {
	r.propagatedObjectsLock.Lock()
	defer r.propagatedObjectsLock.Unlock()

	return len(r.propagatedObjects)
}

// enqueueAllObjects enqueues all the current objects in all namespaces.
func (r *Reconciler) enqueueAllObjects(ctx context.Context, log logr.Logger) error {
	keys := r.Forest.GetNamespaceNames()
	for _, ns := range keys {
		// Enqueue all the current objects in the namespace.
		if err := r.enqueueExistingObjects(ctx, log, ns); err != nil {
			log.Error(err, "Error while trying to enqueue local objects", "namespace", ns)
			return err
		}
	}
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	resp := ctrl.Result{}
	log := logutils.WithRID(r.Log).WithValues("trigger", req.NamespacedName)

	if !config.IsManagedNamespace(req.Namespace) {
		return resp, nil
	}

	if r.Mode == api.Ignore {
		return resp, nil
	}

	stats.StartObjReconcile(r.GVK)
	defer stats.StopObjReconcile(r.GVK)

	// Read the object.
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(r.GVK)
	inst.SetNamespace(req.Namespace)
	inst.SetName(req.Name)
	if err := r.Get(ctx, req.NamespacedName, inst); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Couldn't read")
			return resp, err
		}
	}
	log.V(1).Info("Reconciling")

	// Sync with the forest and perform any required actions.
	actions, srcInst := r.syncWithForest(log, inst)
	return resp, r.operate(ctx, log, actions, inst, srcInst)
}

// syncWithForest syncs the object instance with the in-memory forest. It returns the action to take on
// the object (delete, write or do nothing) and a source object if the action is to write it. It can
// also update the forest if a source object is added or removed.
func (r *Reconciler) syncWithForest(log logr.Logger, inst *unstructured.Unstructured) (syncAction, *unstructured.Unstructured) {
	// This is the only place we should lock the forest in each Reconcile, so this fn needs to return
	// everything relevant for the rest of the Reconcile. This fn shouldn't contact the apiserver since
	// that's a slow operation and everything will block on the lock being held.
	r.Forest.Lock()
	defer r.Forest.Unlock()

	// If this namespace isn't ready to be synced (or is never synced), early exit. We'll be called
	// again if this changes.
	if r.skipNamespace(log, inst) {
		return actionNop, nil
	}

	// If the object's missing and we know how to handle it, return early.
	if missingAction := r.syncMissingObject(log, inst); missingAction != actionUnknown {
		return missingAction, nil
	}

	// Update the forest and get the intended action.
	action, srcInst := r.syncObject(log, inst)

	// If the namespace has a critical condition, we shouldn't actually take any action, regardless of
	// what we'd _like_ to do. We still needed to sync the forest since we want to know when objects
	// are added and removed, so we can sync them properly if the critical condition is resolved, but
	// don't do anything else for now.
	if halted := r.Forest.Get(inst.GetNamespace()).GetHaltedRoot(); halted != "" {
		log.Info("Namespace has 'ActivitiesHalted' condition; will not touch propagated object", "haltedRoot", halted, "suppressedAction", action)
		return actionNop, nil
	}

	return action, srcInst
}

func (r *Reconciler) skipNamespace(log logr.Logger, inst *unstructured.Unstructured) bool {
	// If it's about to be deleted, do nothing, just wait for it to be actually deleted.
	if !inst.GetDeletionTimestamp().IsZero() {
		return true
	}

	ns := r.Forest.Get(inst.GetNamespace())
	// If the object is reconciled before the namespace is synced (when start-up), do nothing.
	if !ns.Exists() {
		log.V(1).Info("Containing namespace hasn't been synced yet")
		return true
	}

	return false
}

// syncMissingObject handles the case where the object we're reconciling doesn't exist. If it can
// determine a final action to take, it returns that action, otherwise it returns actionUnknown
// which indicates that we need to call the regular syncObject method. Note that this method may
// modify `inst`.
func (r *Reconciler) syncMissingObject(log logr.Logger, inst *unstructured.Unstructured) syncAction {
	// If the object exists, skip.
	if inst.GetCreationTimestamp() != (metav1.Time{}) {
		return actionUnknown
	}

	ns := r.Forest.Get(inst.GetNamespace())
	// If it's a source, it must have been deleted. Update the forest and enqueue all its
	// descendants, but there's nothing else to do.
	if ns.HasSourceObject(r.GVK, inst.GetName()) {
		ns.DeleteSourceObject(r.GVK, inst.GetName())
		r.enqueueDescendants(log, inst, "source object is missing and must have been deleted")
		return actionNop
	}

	// This object doesn't exist, and yet someone thinks we need to reconcile it. There are a few
	// reasons why this can happen:
	//
	// 1. This was a source object, and for some reason we got the notification that it's been
	// deleted twice.
	// 2. This is a propagated object. We haven't actually created it yet, but its source exists in
	// the forest so we need to make a copy.
	// 3a. This *was* a propagated object that we've deleted, and we're getting a notification about
	// it.
	// 3b. This *should have been* a propagated object, but due to some error we were never able to
	// create it.
	//
	// In all cases, we're going to give it the api.LabelInherited from label with a dummy value, so
	// that syncObject() treats it as though it's a propagated object. This works well in all three
	// cases because:
	//
	// 1. If this was a source object, but we treat it as a propagated object, we'll see that
	// there's no source in the tree and so there will be nothing to do (which is correct).
	// 2. If this really is a propagated object that needs to be created, we'll find the source in
	// the tree and call write(), which will set the correct value for LabelInheritedFrom.
	// 3. If this *was* a propagated object that's been deleted, then we'll see there's no source
	// (like in case #1) and ignore it.
	metadata.SetLabel(inst, api.LabelInheritedFrom, unknownSourceNamespace)

	// Continue the regular syncing process
	return actionUnknown
}

// syncObject determines if this object is a source or propagated copy and handles it accordingly.
func (r *Reconciler) syncObject(log logr.Logger, inst *unstructured.Unstructured) (syncAction, *unstructured.Unstructured) {
	// If for some reason this has been called on an object that isn't namespaced, let's generate some
	// logspam!
	if inst.GetNamespace() == "" {
		for i := 0; i < 100; i++ {
			log.Info("Non-namespaced object!!!")
		}
		return actionNop, nil
	}

	// If the object should be propagated, we will sync it as an propagated object.
	if yes, srcInst := r.shouldSyncAsPropagated(log, inst); yes {
		return r.syncPropagated(inst, srcInst)
	}

	r.syncSource(log, inst)
	// No action needs to take on source objects.
	return actionNop, nil
}

// shouldSyncAsPropagated returns true and the source object if this object
// should be synced as a propagated object. Otherwise, return false and nil.
// Please note 'srcInst' could still be nil even if the object should be synced
// as propagated, which indicates the source is deleted and so should this object.
func (r *Reconciler) shouldSyncAsPropagated(log logr.Logger, inst *unstructured.Unstructured) (bool, *unstructured.Unstructured) {
	// Get the source object if it exists.
	srcInst := r.getTopSourceToPropagate(log, inst)

	// This object is a propagated copy if it has "api.LabelInheritedFrom" label.
	if hasPropagatedLabel(inst) {
		// Return the top source object of the same name in the ancestors that
		// should propagate.
		return true, srcInst
	}

	// If there's a conflicting source in the ancestors (excluding itself) and the
	// the type has 'Propagate' mode or 'AllowPropagate' mode, the object will be overwritten.
	mode := r.Forest.GetTypeSyncer(r.GVK).GetMode()
	if mode == api.Propagate || mode == api.AllowPropagate {
		if srcInst != nil {
			log.Info("Conflicting object found in ancestors namespace; will overwrite this object", "conflictingAncestor", srcInst.GetNamespace())
			return true, srcInst
		}
	}

	return false, nil
}

// getTopSourceToPropagate returns the top source in the ancestors (excluding
// itself) that can propagate. Otherwise, return nil.
func (r *Reconciler) getTopSourceToPropagate(log logr.Logger, inst *unstructured.Unstructured) *unstructured.Unstructured {
	ns := r.Forest.Get(inst.GetNamespace())
	// Get all the source objects with the same name in the ancestors excluding
	// itself from top down.
	nnms := ns.Parent().GetAncestorSourceNames(r.GVK, inst.GetName())
	for _, nnm := range nnms {
		// If the source cannot propagate, ignore it.
		// TODO: add a webhook rule to prevent e.g. removing a source finalizer that
		//  would cause overwriting the source objects in the descendents.
		//  See https://github.com/kubernetes-sigs/multi-tenancy/issues/1120
		obj := r.Forest.Get(nnm.Namespace).GetSourceObject(r.GVK, nnm.Name)
		if !r.shouldPropagateSource(log, obj, inst.GetNamespace()) {
			continue
		}
		return obj
	}
	return nil
}

// syncPropagated will determine whether to delete the obsolete copy or overwrite it with the source.
// Or do nothing if it remains the same as the source object.
func (r *Reconciler) syncPropagated(inst, srcInst *unstructured.Unstructured) (syncAction, *unstructured.Unstructured) {
	ns := r.Forest.Get(inst.GetNamespace())
	// Delete this local source object from the forest if it exists. (This could
	// only happen when we are trying to overwrite a conflicting source).
	ns.DeleteSourceObject(r.GVK, inst.GetName())
	stats.OverwriteObject(r.GVK)

	// If no source object exists, delete this object. This can happen when the source was deleted by
	// users or the admin decided this type should no longer be propagated.
	if srcInst == nil {
		return actionRemove, nil
	}

	// If an object doesn't exist, assume it's been deleted or not yet created.
	exists := inst.GetCreationTimestamp() != metav1.Time{}

	// If the copy does not exist, or is different from the source, return the write action and the
	// source instance. Note that DeepEqual could return `true` even if the object doesn't exist if
	// the source object is trivial (e.g. a completely empty ConfigMap).
	if !exists ||
		!reflect.DeepEqual(canonical(inst), canonical(srcInst)) ||
		inst.GetLabels()[api.LabelInheritedFrom] != srcInst.GetNamespace() {
		metadata.SetLabel(inst, api.LabelInheritedFrom, srcInst.GetNamespace())
		return actionWrite, srcInst
	}

	// The object already exists and doesn't need to be updated. This will typically happen when HNC
	// is restarted - all the propagated objects already exist on the apiserver. Record that it exists
	// for our statistics.
	r.recordPropagatedObject(inst.GetNamespace(), inst.GetName())

	// Nothing more needs to be done.
	return actionNop, nil
}

// syncSource updates the copy in the forest with the current source object. We
// will enqueue all the descendants if the source can be propagated. The
// propagation exceptions will be checked when reconciling the (enqueued)
// propagated objects.
func (r *Reconciler) syncSource(log logr.Logger, src *unstructured.Unstructured) {
	// Update or create a copy of the source object in the forest. We now store
	// all the source objects in the forests no matter if the mode is 'Propagate'
	// or not, because HNCConfig webhook will also check the non-'Propagate' or non-'AllowPropagate' modes
	// source objects in the forest to see if a mode change is allowed.
	ns := r.Forest.Get(src.GetNamespace())

	ns.SetSourceObject(cleanSource(src))

	// Enqueue propagated copies for this possibly deleted source
	r.enqueueDescendants(log, src, "modified source object")
}

// cleanSource creates a sanitized version of the object to store in the forest. In particular, it
// sets the standard app.kubernetes.io/managed-by label, and cleans out any unpropagated
// annotations.
func cleanSource(src *unstructured.Unstructured) *unstructured.Unstructured {
	// Don't modify the original
	src = src.DeepCopy()

	// Clean out bad annotations.
	annot := src.GetAnnotations()
	if annot == nil {
		annot = map[string]string{}
	}
	for _, unprop := range config.UnpropagatedAnnotations {
		delete(annot, unprop)
	}
	src.SetAnnotations(annot)

	// Set or replace the managed-by label.
	labels := src.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[api.LabelManagedByApps] = api.MetaGroup

	// Clean out bad labels.
	for _, unprop := range config.UnpropagatedLabels {
		delete(labels, unprop)
	}
	src.SetLabels(labels)

	return src
}

func (r *Reconciler) enqueueDescendants(log logr.Logger, src *unstructured.Unstructured, reason string) {
	sns := r.Forest.Get(src.GetNamespace())
	if halted := sns.GetHaltedRoot(); halted != "" {
		// There's no point enqueuing anything if the source namespace is halted since we'll just skip
		// any action anyway.
		log.V(1).Info("Will not enqueue descendants since activities are halted", "haltedRoot", halted, "oldReason", reason)
		return
	}
	log.V(1).Info("Enqueuing descendant objects", "reason", reason)
	for _, ns := range sns.DescendantNames() {
		dc := canonical(src)
		dc.SetNamespace(ns)
		log.V(1).Info("... enqueuing descendant copy", "affected", ns+"/"+src.GetName(), "reason", reason)
		r.Affected <- event.GenericEvent{Object: dc}
	}
}

// enqueueExistingObjects enqueues all the objects (with the same GVK) in the namespace.
//
// This method actually does an apiserver (or cache) List operation on all objects in the namespace.
// This will result in a lot of overlap with enqueueSourcesAndPotentialCopies (especially the
// sources in this namespace), but it will catch any _stale_ propagated objects in this namespace
// that no longer have a source in an ancestor namespace, which that function will miss. This can
// happen either if the source has been deleted or (more likely in the context of
// OnChangedNamespace) the ancestors of this namespace have changed and so the old sources no longer
// exist.
//
// Since this function is called while the forest lock is held, everything it does is launched in a
// new goroutine to avoid any possible calls to the apiserver while the lock is held.
func (r *Reconciler) enqueueExistingObjects(ctx context.Context, log logr.Logger, nsnm string) error {
	ul := &unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(r.GVK)
	ul.SetKind(ul.GetKind() + "List")
	if err := r.List(ctx, ul, client.InNamespace(nsnm)); err != nil {
		return err
	}

	for _, inst := range ul.Items {
		// We don't need the entire canonical object here but only its metadata.
		// Using canonical copy is the easiest way to get an object with its metadata set.
		co := canonical(&inst)
		co.SetNamespace(inst.GetNamespace())
		log.V(1).Info("Enqueuing existing object for reconciliation", "affected", co.GetName())
		r.Affected <- event.GenericEvent{Object: co}
	}

	return nil
}

// operate operates the action generated from syncing the object with the forest.
func (r *Reconciler) operate(ctx context.Context, log logr.Logger, act syncAction, inst, srcInst *unstructured.Unstructured) error {
	var err error

	switch act {
	case actionNop:
		// nop
	case actionRemove:
		err = r.deleteObject(ctx, log, inst)
	case actionWrite:
		err = r.writeObject(ctx, log, inst, srcInst)
	default: // this should never, ever happen. But if it does, try to make a very obvious error message.
		if act == "" {
			act = actionUnknown
		}
		err = fmt.Errorf("HNC couldn't determine how to update this object (desired action: %s)", act)
	}

	r.generateEvents(log, srcInst, inst, act, err)
	return err
}

func (r *Reconciler) deleteObject(ctx context.Context, log logr.Logger, inst *unstructured.Unstructured) error {

	stats.WriteObject(r.GVK)
	err := r.Delete(ctx, inst)
	if errors.IsNotFound(err) {
		log.V(1).Info("The obsolete copy doesn't exist, no more action needed")
		return nil
	}

	if err != nil {
		// Don't log the error since controller-runtime will do it for us
		return err
	}

	log.Info("Deleted propagated object")
	// Remove the propagated object from the map because we are confident that the object was successfully deleted
	// on the apiserver.
	r.recordRemovedObject(inst.GetNamespace(), inst.GetName())
	return nil
}

func (r *Reconciler) writeObject(ctx context.Context, log logr.Logger, inst, srcInst *unstructured.Unstructured) error {
	// The object exists if CreationTimestamp is set. This flag enables us to have only 1 API call.
	exist := inst.GetCreationTimestamp() != metav1.Time{}
	ns := inst.GetNamespace()
	// Get current ResourceVersion, required for updates for newer API objects (including custom resources).
	rv := inst.GetResourceVersion()

	// Overwrite the propagated copy with the source, then restore all essential properties of the copy.
	inst = canonical(srcInst)
	inst.SetNamespace(ns)
	// We should set the resourceVersion when updating the object, which is required by k8s.
	// for more details see https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/53.
	inst.SetResourceVersion(rv)
	metadata.SetLabel(inst, api.LabelInheritedFrom, srcInst.GetNamespace())
	log.V(1).Info("Writing", "dst", inst.GetNamespace(), "origin", srcInst.GetNamespace())

	var err error = nil
	stats.WriteObject(r.GVK)
	if exist {
		log.Info("Updating propagated object")
		err = r.Update(ctx, inst)
		// RoleBindings can't have their Roles changed after they're created
		// (see  https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/798).
		// If an RB was quickly delete and re-created in an ancestor namespace
		// - fast enough that by the time that HNC notices, the new RB exists; or
		// if there's a change to the RBs when HNC isn't running - HNC could see
		// it as an update (not a delete + create) and attempt to update the RBs in
		// all descendant namespaces, and this will fail. In order to handle this
		// case, we try to delete and re-create the rolebinding here

		// We don't apply this logic to other objects because if another object has an
		// ownerReference pointing to the object we're deleting, it could be deleted as
		// well, which is undesirable.

		// The error type is 'Invalid' after I tested it out with different error types
		// from https://godoc.org/k8s.io/apimachinery/pkg/api/errors
		group := inst.GroupVersionKind().Group
		if err != nil && errors.IsInvalid(err) && inst.GetKind() == "RoleBinding" && group == "rbac.authorization.k8s.io" {
			// Log this error because we're about to throw it away.
			log.Error(err, "Couldn't update propagated object; will try to delete and recreate instead")
			if err = r.Delete(ctx, inst); err == nil {
				err = r.Create(ctx, inst)
				if err != nil {
					log.Info("Couldn't recreate propagated object after deleting it") // error is handles below
				} else {
					log.Info("Successfully recreated propagated object")
				}
			} else {
				log.Info("Couldn't delete propagated object that we couldn't update") // error is handles below
			}
		}
	} else {
		log.Info("Propagating object")
		err = r.Create(ctx, inst)
	}
	if err != nil {
		// Don't log the error since controller-runtime will do it for us
		return err
	}

	// Add the object to the map if it does not exist because we are confident that the object was updated/created
	// successfully on the apiserver.
	r.recordPropagatedObject(inst.GetNamespace(), inst.GetName())
	return nil
}

// generateEvents is called when the reconciler has performed all necessary
// actions and knows if they've succeeded or failed. If a source should not be
// propagated or there was a failure, generate "Warning" events.
func (r *Reconciler) generateEvents(log logr.Logger, srcInst, inst *unstructured.Unstructured, act syncAction, err error) {
	switch {
	case hasFinalizers(inst):
		// Propagated objects can never have finalizers
		log.Info("Generating event", "type", api.EventCannotPropagate, "msg", "Objects with finalizers cannot be propagated")
		r.EventRecorder.Event(inst, "Warning", api.EventCannotPropagate, "Objects with finalizers cannot be propagated")

	case errors.IsAlreadyExists(err):
		// This can happen if the same newly-propagated object is enqueued twice in quick succession;
		// the second goroutine will try to create it but it will already exist. We should definitely
		// retry the reconciliation (after all, something may have changed) but there's no need to log
		// an event since this error isn't expected to be persistent - we can always overwrite if we
		// want to.
		return

	case err != nil:
		// There was an error updating this object; generate a warning pointing to
		// the source object. Note we never take actions on a source object, so only
		// propagated objects could possibly get an error.
		msg := fmt.Sprintf("Could not %s from source namespace %q: %s.", act, inst.GetLabels()[api.LabelInheritedFrom], err.Error())
		log.Info("Generating event", "type", api.EventCannotUpdate, "msg", msg)
		r.EventRecorder.Event(inst, "Warning", api.EventCannotUpdate, msg)

		// Also generate a warning on the source if one exists.
		if srcInst != nil {
			msg = fmt.Sprintf("Could not %s to destination namespace %q: %s.", act, inst.GetNamespace(), err.Error())
			log.Info("Generating event", "srcNS", srcInst.GetNamespace(), "type", api.EventCannotPropagate, "msg", msg)
			r.EventRecorder.Event(srcInst, "Warning", api.EventCannotPropagate, msg)
		}
	}
}

// hasPropagatedLabel returns true if "api.LabelInheritedFrom" label is set.
func hasPropagatedLabel(inst *unstructured.Unstructured) bool {
	labels := inst.GetLabels()
	if labels == nil {
		// this cannot be a copy
		return false
	}
	_, po := labels[api.LabelInheritedFrom]
	return po
}

// shouldPropagateSource returns true if the object should be propagated by the HNC. The following
// objects are not propagated:
// - Objects of a type whose mode is set to "remove" in the HNCConfiguration singleton
// - Objects with nonempty finalizer list
// - Objects have a selector that doesn't match the destination namespace
// - Service Account token secrets
func (r *Reconciler) shouldPropagateSource(log logr.Logger, inst *unstructured.Unstructured, dst string) bool {
	nsLabels := r.Forest.Get(dst).GetLabels()
	if ok, err := selectors.ShouldPropagate(inst, nsLabels, r.Mode); err != nil {
		log.Error(err, "Cannot propagate")
		r.EventRecorder.Event(inst, "Warning", api.EventCannotParseSelector, err.Error())
		return false
	} else if !ok {
		return false
	}

	switch {
	// Users can set the mode of a type to "remove" to exclude objects of the type
	// from being handled by HNC.
	case r.Mode == api.Remove:
		return false

	// Object with nonempty finalizer list is not propagated
	case hasFinalizers(inst):
		return false

	case r.GVK.Group == "" && r.GVK.Kind == "Secret":
		// These are reaped by a builtin K8s controller so there's no point copying them.
		// More to the point, SA tokens really aren't supposed to be copied between
		// namespaces. You *could* make the argument that a parent namespace's SA should be
		// shared with all its descendants, but you could also make the case that while
		// administration should be inherited, identity should not. At any rate, it's moot
		// as long as K8s auto deletes these tokens, and we shouldn't fight K8s.
		if inst.UnstructuredContent()["type"] == "kubernetes.io/service-account-token" {
			log.V(1).Info("Excluding: service account token")
			return false
		}
		return true

	default:
		// Everything else is propagated
		return true
	}
}

// recordPropagatedObject records the fact that this object has been propagated, so we can report
// statistics in the HNC Config.
func (r *Reconciler) recordPropagatedObject(namespace, name string) {
	r.propagatedObjectsLock.Lock()
	defer r.propagatedObjectsLock.Unlock()

	nnm := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	if !r.propagatedObjects[nnm] {
		r.propagatedObjects[nnm] = true
	}
}

// recordRemovedObject records the fact that this (possibly) previously propagated object no longer
// exists.
func (r *Reconciler) recordRemovedObject(namespace, name string) {
	r.propagatedObjectsLock.Lock()
	defer r.propagatedObjectsLock.Unlock()

	nnm := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	if r.propagatedObjects[nnm] {
		delete(r.propagatedObjects, nnm)
	}
}

func hasFinalizers(inst *unstructured.Unstructured) bool {
	return len(inst.GetFinalizers()) != 0
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, maxReconciles int) error {
	r.propagatedObjects = namespacedNameSet{}
	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(r.GVK)
	opts := controller.Options{
		MaxConcurrentReconciles: maxReconciles,

		// Unlike the other HNC reconcilers, the object reconciler can easily be affected by objects
		// that do not directly cause reconciliations when they're modified - see, e.g.,
		// https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/1154. To address this, replace the
		// default exponential backoff with one with a 10s cap.
		//
		// I wanted to pick five seconds since I feel like that's about how much time it would take you to check
		// to see if something's wrong, realize it hasn't been propagated, and then try again. However,
		// the default etcd timeout is 10s, which apparently is a more realistic measure of how K8s can
		// behave under heavy load, so I raised it to 10s during the PR review. The _average_ delay seen
		// by users should still be about 5s though.
		RateLimiter: workqueue.NewItemExponentialFailureRateLimiter(250*time.Millisecond, 10*time.Second),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(target).
		Watches(&source.Channel{Source: r.Affected}, &handler.EnqueueRequestForObject{}).
		WithOptions(opts).
		Complete(r)
}
