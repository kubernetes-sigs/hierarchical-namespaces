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

package hierarchyconfig

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/crd"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/metadata"
	"sigs.k8s.io/hierarchical-namespaces/internal/stats"
)

// reconcileID is used purely to set the "rid" field in the log, so we can tell which log messages
// were part of the same reconciliation attempt, even if multiple are running parallel (or it's
// simply hard to tell when one ends and another begins).
//
// The ID is shared among all reconcilers so that each log message gets a unique ID across all of
// HNC.
var nextReconcileID int64

// loggerWithRID adds a reconcile ID (rid) to the given logger.
func loggerWithRID(log logr.Logger) logr.Logger {
	rid := atomic.AddInt64(&nextReconcileID, 1)
	return log.WithValues("rid", rid)
}

// Reconciler is responsible for determining the forest structure from the Hierarchy CRs,
// as well as ensuring all objects in the forest are propagated correctly when the hierarchy
// changes. It can also set the status of the Hierarchy CRs, as well as (in rare cases) override
// part of its spec (i.e., if a parent namespace no longer exists).
type Reconciler struct {
	client.Client
	Log logr.Logger

	// Forest is the in-memory data structure that is shared with all other reconcilers.
	// Reconciler is responsible for keeping it up-to-date, but the other reconcilers
	// use it to determine how to propagate objects.
	Forest *forest.Forest

	// Affected is a channel of event.GenericEvent (see "Watching Channels" in
	// https://book-v1.book.kubebuilder.io/beyond_basics/controller_watches.html) that is used to
	// enqueue additional namespaces that need updating.
	Affected chan event.GenericEvent

	// These are interfaces to the other reconcilers
	AnchorReconciler    AnchorReconcilerType
	HNCConfigReconciler HNCConfigReconcilerType
}

type AnchorReconcilerType interface {
	Enqueue(log logr.Logger, ns, parent, msg string)
}

type HNCConfigReconcilerType interface {
	Enqueue(msg string)
}

// +kubebuilder:rbac:groups=hnc.x-k8s.io,resources=hierarchies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hnc.x-k8s.io,resources=hierarchies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch

// Reconcile sets up some basic variables and then calls the business logic.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ns := req.NamespacedName.Namespace
	log := loggerWithRID(r.Log).WithValues("ns", ns)

	// Early exit if it's an excluded namespace
	if !config.IsManagedNamespace(ns) {
		return ctrl.Result{}, r.handleUnmanaged(ctx, log, ns)
	}

	stats.StartHierConfigReconcile()
	defer stats.StopHierConfigReconcile()

	return ctrl.Result{}, r.reconcile(ctx, log, ns)
}

func (r *Reconciler) handleUnmanaged(ctx context.Context, log logr.Logger, nm string) error {
	// Get the namespace. Early exist if the namespace doesn't exist or is purged.
	// If so, there must be no namespace label or HC instance to delete.
	nsInst, err := r.getNamespace(ctx, nm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Remove the "included-namespace" label on excluded namespace if it exists.
	origNS := nsInst.DeepCopy()
	r.removeIncludedNamespaceLabel(log, nsInst)
	if _, err := r.writeNamespace(ctx, log, origNS, nsInst); err != nil {
		return err
	}

	// Don't delete the hierarchy config, since the admin might be enabling and disabling the regex and
	// we don't want this to be destructive. Instead, just remove the finalizers if there are any so that
	// users can delete it if they like.
	inst, _, err := r.getSingleton(ctx, nm)
	if err != nil {
		return err
	}
	if len(inst.ObjectMeta.Finalizers) > 0 || len(inst.Status.Children) > 0 {
		log.Info("Removing finalizers and children on unmanaged singleton")
		inst.ObjectMeta.Finalizers = nil
		inst.Status.Children = nil
		stats.WriteHierConfig()
		if err := r.Update(ctx, inst); err != nil {
			log.Error(err, "while removing finalizers on unmanaged namespace")
			return err
		}
	}

	return nil
}

func (r *Reconciler) reconcile(ctx context.Context, log logr.Logger, nm string) error {
	// Load the namespace and make a copy
	nsInst, err := r.getNamespace(ctx, nm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The namespace doesn't exist or is purged. Update the forest and exit.
			// (There must be no HC instance and we cannot create one.)
			r.onMissingNamespace(log, nm)
			return nil
		}
		return err
	}
	origNS := nsInst.DeepCopy()

	// Add the "included-namespace" label to the namespace if it doesn't exist. The
	// excluded namespaces should be already skipped earlier.
	r.addIncludedNamespaceLabel(log, nsInst)

	// Get singleton from apiserver. If it doesn't exist, initialize one.
	inst, deletingCRD, err := r.getSingleton(ctx, nm)
	if err != nil {
		return err
	}
	// Don't _create_ the singleton if its CRD is being deleted. But if the singleton is already
	// _present_, we may need to update it to remove finalizers (see #824).
	if deletingCRD && inst.CreationTimestamp.IsZero() {
		log.Info("HierarchyConfiguration CRD is being deleted; will not sync")
		return nil
	}
	origHC := inst.DeepCopy()

	// Get a list of subnamespace anchors from apiserver.
	anms, err := r.getAnchorNames(ctx, nm)
	if err != nil {
		return err
	}

	parentAnchor, err := r.getSubnamespaceAnchorInstance(ctx, nsInst.Annotations[api.SubnamespaceOf], nm)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// Update whether the HC is deletable.
	r.updateFinalizers(log, inst, nsInst, anms)

	// Sync the Hierarchy singleton with the in-memory forest.
	needUpdateObjects := r.syncWithForest(log, nsInst, inst, deletingCRD, anms, parentAnchor)

	// Write back if anything's changed. Early-exit if we just write back exactly what we had and this
	// isn't the first time we're syncing.
	updated, err := r.writeInstances(ctx, log, origHC, inst, origNS, nsInst)
	needUpdateObjects = updated || needUpdateObjects
	if !needUpdateObjects || err != nil {
		return err
	}

	// Update all the objects in this namespace. We have to do this at least *after* the tree is
	// updated, because if we don't, we could incorrectly think we've propagated the wrong objects
	// from our ancestors, or are propagating the wrong objects to our descendants.
	//
	// NB: if writeInstance didn't actually write anything - that is, if the hierarchy didn't change -
	// this update is skipped. Otherwise, we can get into infinite loops because both objects and
	// hierarchy reconcilers are enqueuing too freely. TODO: only call updateObjects when we make the
	// *kind* of changes that *should* cause objects to be updated (eg add/remove the ActivitiesHalted
	// condition, change subtree parents, etc).
	return r.updateObjects(ctx, log, nm)
}

func (r *Reconciler) onMissingNamespace(log logr.Logger, nm string) {
	r.Forest.Lock()
	defer r.Forest.Unlock()
	ns := r.Forest.Get(nm)

	if ns.Exists() {
		r.enqueueAffected(log, "relative of deleted namespace", ns.RelativesNames()...)
		ns.UnsetExists()
		log.Info("Namespace has been deleted")
	}
}

// removeIncludedNamespaceLabel removes the `hnc.x-k8s.io/included-namespace`
// label from the namespace.
func (r *Reconciler) removeIncludedNamespaceLabel(log logr.Logger, nsInst *corev1.Namespace) {
	lbs := nsInst.Labels
	if _, found := lbs[api.LabelIncludedNamespace]; found {
		log.Info("Illegal included-namespace label found; removing")
		delete(lbs, api.LabelIncludedNamespace)
		nsInst.SetLabels(lbs)
	}
}

// addIncludedNamespaceLabel adds the `hnc.x-k8s.io/included-namespace` label to the
// namespace.
func (r *Reconciler) addIncludedNamespaceLabel(log logr.Logger, nsInst *corev1.Namespace) {
	lbs := nsInst.Labels
	if lbs == nil {
		lbs = make(map[string]string)
	}
	if v, found := lbs[api.LabelIncludedNamespace]; found && v == "true" {
		return
	}
	log.Info("Adding included-namespace label")
	lbs[api.LabelIncludedNamespace] = "true"
	nsInst.SetLabels(lbs)
}

// updateFinalizers ensures that the HC can't be deleted if there are any subnamespace anchors in
// the namespace (with some exceptions). The main reason is that the HC stores the
// .spec.allowCascadingDeletion field, so if we allowed this object to be deleted before all
// descendants have been deleted, we would lose the knowledge that cascading deletion is enabled.
func (r *Reconciler) updateFinalizers(log logr.Logger, inst *api.HierarchyConfiguration, nsInst *corev1.Namespace, anms []string) {
	// No-one should put a finalizer on a hierarchy config except us. See
	// https://github.com/kubernetes-sigs/hierarchical-namespaces/issues/623 as we try to enforce that.
	switch {
	case len(anms) == 0:
		// There are no subnamespaces in this namespace. The HC instance can be safely deleted anytime.
		if len(inst.ObjectMeta.Finalizers) > 0 {
			log.V(1).Info("Removing finalizers since there are no longer any anchors in the namespace.")
		}
		inst.ObjectMeta.Finalizers = nil
	case !inst.DeletionTimestamp.IsZero() && nsInst.DeletionTimestamp.IsZero():
		// If the HC instance is being deleted but not the namespace (which means
		// it's not a cascading delete), remove the finalizers to let it go through.
		// This is the only case the finalizers can be removed even when the
		// namespace has subnamespaces. (A default HC will be recreated later.)
		log.Info("Removing finalizers to allow a single deletion of the singleton (not involved in a cascading deletion).")
		inst.ObjectMeta.Finalizers = nil
	default:
		if len(inst.ObjectMeta.Finalizers) == 0 {
			log.Info("Adding finalizers since there's at least one anchor in the namespace.")
		}
		inst.ObjectMeta.Finalizers = []string{api.FinalizerHasSubnamespace}
	}
}

// syncWithForest synchronizes the in-memory forest with the (in-memory) Hierarchy instance. If any
// *other* namespaces have changed, it enqueues them for later reconciliation. This method is
// guarded by the forest mutex, which means that none of the other namespaces being reconciled will
// be able to proceed until this one is finished. While the results of the reconiliation may not be
// fully written back to the apiserver yet, each namespace is reconciled in isolation (apart from
// the in-memory forest) so this is fine. Return true, if the namespace is just synced or the
// namespace labels are changed that requires updating all objects in the namespaces.
func (r *Reconciler) syncWithForest(log logr.Logger, nsInst *corev1.Namespace, inst *api.HierarchyConfiguration, deletingCRD bool, anms []string, parentAnchor *api.SubnamespaceAnchor) bool {
	r.Forest.Lock()
	defer r.Forest.Unlock()
	ns := r.Forest.Get(inst.ObjectMeta.Namespace)

	// Clear all conditions; we'll re-add them if they're still relevant. But first, record whether
	// this namespace (excluding ancestors) was halted, since if that changes, we'll need to notify
	// other namespaces.
	wasHalted := ns.IsHalted()
	ns.ClearConditions()
	// We can figure out this condition pretty easily...
	if deletingCRD {
		ns.SetCondition(api.ConditionActivitiesHalted, api.ReasonDeletingCRD, "The HierarchyConfiguration CRD is being deleted; all propagation is disabled.")
	}

	// Set external tree labels in the forest if this is an external namespace.
	r.syncExternalNamespace(log, nsInst, ns)

	// If this is a subnamespace, make sure .spec.parent is set correctly. Then sync the parent to the
	// forest, and finally notify any relatives (including the parent) that might have been waiting
	// for this namespace to be synced.
	r.syncSubnamespaceParent(log, inst, nsInst, ns, parentAnchor)
	r.syncParent(log, inst, ns)
	initial := r.markExisting(log, ns)

	// Sync labels and annotations, now that the structure's been updated.
	updatedLabels := r.syncLabels(log, inst, nsInst, ns)
	r.syncAnnotations(log, inst, nsInst, ns)

	// Sync other spec and spec-like info
	r.syncAnchors(log, ns, anms)
	if ns.UpdateAllowCascadingDeletion(inst.Spec.AllowCascadingDeletion) {
		// Added to help debug #1155 if it ever reoccurs
		log.Info("Updated allowCascadingDeletion", "newValue", inst.Spec.AllowCascadingDeletion)
	}

	// Sync the status
	inst.Status.Children = ns.ChildNames()
	r.syncConditions(log, inst, ns, wasHalted)

	r.HNCConfigReconciler.Enqueue("namespace reconciled")
	return initial || updatedLabels
}

// syncExternalNamespace sets external tree labels to the namespace in the forest
// if the namespace is an external namespace not managed by HNC.
func (r *Reconciler) syncExternalNamespace(log logr.Logger, nsInst *corev1.Namespace, ns *forest.Namespace) {
	mgr := nsInst.Annotations[api.AnnotationManagedBy]
	if mgr == "" || mgr == api.MetaGroup {
		// If the internal namespace is just converted from an external namespace,
		// enqueue the descendants to remove the propagated external tree labels.
		if ns.IsExternal() {
			r.enqueueAffected(log, "subtree root converts from external to internal", ns.DescendantNames()...)
		}
		ns.Manager = api.MetaGroup
		return
	}

	// If the external namespace is just converted from an internal namespace,
	// enqueue the descendants to propagate the external tree labels.
	if !ns.IsExternal() {
		r.enqueueAffected(log, "subtree root converts from internal to external", ns.DescendantNames()...)
	}
	ns.Manager = mgr
}

// syncSubnamespaceParent sets the parent to the owner and updates the SubnamespaceAnchorMissing
// condition if the anchor is missing in the parent namespace according to the forest. The
// subnamespace-of annotation is the source of truth of the ownership (e.g. being a subnamespace),
// since modifying a namespace has higher privilege than what HNC users can do.
func (r *Reconciler) syncSubnamespaceParent(log logr.Logger, inst *api.HierarchyConfiguration, nsInst *corev1.Namespace, ns *forest.Namespace, parentAnchor *api.SubnamespaceAnchor) {
	if ns.IsExternal() {
		ns.IsSub = false
		return
	}

	pnm := nsInst.Annotations[api.SubnamespaceOf]

	// Issue #1130: as a subnamespace is being deleted (e.g. because its anchor was deleted), ignore
	// the annotation. K8s will remove the HC, which will effectively orphan this namespace, prompting
	// HNC to remove all propagated objects, allowing it to be deleted cleanly. Without this, HNC
	// would continue to think that all propagated objects needed to be protected from deletion and
	// would prevent K8s from emptying and removing the namespace.
	//
	// We could also add an exception to allow K8s SAs to override the object validator (and we
	// probably should), but this prevents us from getting into a war with K8s and is sufficient for
	// v0.5.
	if pnm != "" && !nsInst.DeletionTimestamp.IsZero() {
		log.V(1).Info("Subnamespace is being deleted; ignoring SubnamespaceOf annotation", "parent", inst.Spec.Parent, "annotation", pnm)
		pnm = ""
	}

	if pnm == "" {
		ns.IsSub = false
		return
	}
	ns.IsSub = true

	if inst.Spec.Parent != pnm {
		if inst.Spec.Parent == "" {
			// Print a friendlier log message for when a subns is first reconciled. Technically, a user
			// _could_ manually clear the parent field, in which case they'll see this message again, but
			// that's enough of a corner case that it's probably not worth fixing.
			log.Info("Inserting newly created subnamespace into the hierarchy", "parent", pnm)
		} else {
			log.Info("The parent doesn't match the subnamespace annotation; overwriting parent", "oldParent", inst.Spec.Parent, "parent", pnm)
		}
		inst.Spec.Parent = pnm
	}

	if parentAnchor == nil {
		ns.SetCondition(api.ConditionBadConfiguration, api.ReasonAnchorMissing, "The anchor is missing in the parent namespace")
	} else {
		inst.Spec.Labels = parentAnchor.Spec.Labels
		inst.Spec.Annotations = parentAnchor.Spec.Annotations
	}
}

// markExisting marks the namespace as existing. If this is the first time we're reconciling this namespace,
// mark all possible relatives as being affected since they may have been waiting for this namespace.
func (r *Reconciler) markExisting(log logr.Logger, ns *forest.Namespace) bool {
	if !ns.SetExists() {
		return false
	}
	log.Info("New namespace found")
	r.enqueueAffected(log, "relative of newly found namespace", ns.RelativesNames()...)
	if ns.IsSub {
		r.enqueueAffected(log, "parent of the newly found subnamespace", ns.Parent().Name())
		r.AnchorReconciler.Enqueue(log, ns.Name(), ns.Parent().Name(), "the missing subnamespace is found")
	}
	return true
}

func (r *Reconciler) syncParent(log logr.Logger, inst *api.HierarchyConfiguration, ns *forest.Namespace) {
	// As soon as the structure has been updated, do a cycle check.
	defer r.setCycleCondition(log, ns)

	if ns.IsExternal() {
		ns.SetParent(nil)
		return
	}

	// Sync this namespace with its current parent.
	curParent := r.Forest.Get(inst.Spec.Parent)
	if !config.IsManagedNamespace(inst.Spec.Parent) {
		log.Info("Setting ConditionActivitiesHalted: unmanaged namespace set as parent", "parent", inst.Spec.Parent)
		ns.SetCondition(api.ConditionActivitiesHalted, api.ReasonIllegalParent, fmt.Sprintf("Parent %q is an unmanaged namespace", inst.Spec.Parent))
	} else if curParent != nil && !curParent.Exists() {
		log.Info("Setting ConditionActivitiesHalted: parent doesn't exist (or hasn't been synced yet)", "parent", inst.Spec.Parent)
		ns.SetCondition(api.ConditionActivitiesHalted, api.ReasonParentMissing, fmt.Sprintf("Parent %q does not exist", inst.Spec.Parent))
	}

	// If the parent hasn't changed, there's nothing more to do.
	oldParent := ns.Parent()
	if curParent == oldParent {
		return
	}

	// If this namespace *was* involved in a cycle, enqueue all elements in that cycle in the hopes
	// we're about to break it.
	r.enqueueAffected(log, "member of a cycle", ns.CycleNames()...)

	// Change the parent.
	ns.SetParent(curParent)

	// Enqueue all other namespaces that could be directly affected. The old and new parents have just
	// gained/lost a child, while the descendants need to have their tree labels updated and their
	// objects resynced. Note that it's fine if oldParent or curParent is nil - see enqueueAffected
	// for details.
	//
	// If we've just created a cycle, all the members of that cycle will be listed as the descendants,
	// so enqueuing them will ensure that the conditions show up in all members of the cycle.
	r.enqueueAffected(log, "removed as parent", oldParent.Name())
	r.enqueueAffected(log, "set as parent", curParent.Name())
	r.enqueueAffected(log, "subtree root has changed", ns.DescendantNames()...)
}

func (r *Reconciler) setCycleCondition(log logr.Logger, ns *forest.Namespace) {
	cycle := ns.CycleNames()
	if cycle == nil {
		return
	}

	msg := fmt.Sprintf("Namespace is a member of the cycle: %s", strings.Join(cycle, " <- "))
	log.Info(msg)
	ns.SetCondition(api.ConditionActivitiesHalted, api.ReasonInCycle, msg)
}

// syncAnchors updates the anchor list. If any anchor is created/deleted, it will enqueue
// the child to update its SubnamespaceAnchorMissing condition. A modified anchor will appear
// twice in the change list (one in deleted, one in created), both subnamespaces
// needs to be enqueued in this case.
func (r *Reconciler) syncAnchors(log logr.Logger, ns *forest.Namespace, anms []string) {
	for _, changedAnchors := range ns.SetAnchors(anms) {
		r.enqueueAffected(log, "SubnamespaceAnchorMissing condition may have changed due to anchor being created/deleted", changedAnchors)
	}
}

// Sync namespace managed and tree labels. Return true if the labels are updated, so we know to
// re-sync all objects that could be affected by exclusions.
func (r *Reconciler) syncLabels(log logr.Logger, inst *api.HierarchyConfiguration, nsInst *corev1.Namespace, ns *forest.Namespace) bool {
	// Get a list of all managed labels from the config object.
	managed := map[string]string{}
	for _, kvp := range inst.Spec.Labels {
		if !config.IsManagedLabel(kvp.Key) {
			log.Info("Illegal managed label", "key", kvp.Key)
			ns.SetCondition(api.ConditionBadConfiguration, api.ReasonIllegalManagedLabel, "Not a legal managed label (set via --managed-namespace-label): "+kvp.Key)
			continue
		}
		managed[kvp.Key] = kvp.Value
	}

	// External namespaces can also have both managed and tree labels set directly. All other
	// namespaces must have these removed now.
	for k, v := range nsInst.Labels {
		if config.IsManagedLabel(k) {
			if ns.IsExternal() {
				managed[k] = v
			} else {
				delete(nsInst.Labels, k)
			}
		}
		if !ns.IsExternal() && strings.HasSuffix(k, api.LabelTreeDepthSuffix) {
			delete(nsInst.Labels, k)
		}
	}
	if len(managed) == 0 {
		// Don't cause unnecessary updates from the default value
		managed = nil
	}

	// Record all managed labels in the forest. If they've changed, we need to enqueue all descendants
	// to propagate the changes.
	if !reflect.DeepEqual(ns.ManagedLabels, managed) {
		ns.ManagedLabels = managed
		log.Info("Updated managed label", "newLabels", managed)
		r.enqueueAffected(log, "managed labels have changed", ns.DescendantNames()...)
	}

	// For external namespaces, we're pretty much done since HNC mainly doesn't manage the metadata of
	// external namespaces. Just make sure the zero-depth tree label is set if it wasn't already, and
	// store all its labels so the tree labels can be retrieved later.
	if ns.IsExternal() {
		metadata.SetLabel(nsInst, nsInst.Name+api.LabelTreeDepthSuffix, "0")
		ns.SetLabels(nsInst.Labels)

		// There are no propagated objects in external namespaces so we don't need to notify the caller
		// that any propagation-relevant labels have changed.
		return false
	}

	// Set all managed and tree labels, starting from the current namespace and going up through the
	// hierarchy.
	curNS := ns
	depth := 0
	for curNS != nil {
		// Set the tree label from this layer of hierarchy
		metadata.SetLabel(nsInst, curNS.Name()+api.LabelTreeDepthSuffix, strconv.Itoa(depth))

		// Add any managed labels. TODO: add conditions for conflicts.
		for k, v := range curNS.ManagedLabels {
			log.V(1).Info("Setting managed label", "from", curNS.Name(), "key", k, "value", v)
			metadata.SetLabel(nsInst, k, v)
		}

		// If the root is an external namespace, add all its external tree labels too.
		// Note it's impossible to have an external namespace as a non-root, which is
		// enforced by both admission controllers and the reconciler here.
		if curNS.IsExternal() {
			for k, v := range curNS.GetTreeLabels() {
				metadata.SetLabel(nsInst, k, strconv.Itoa(depth+v))
			}

			// Note that it's impossible to have an external namespace as a non-root (enforced elsewhere)
			// so technically we don't need to break out of the loop here. But I find it cleaner.
			break
		}

		// Stop if this namespace is halted, which could indicate a cycle or orphan.
		if curNS.IsHalted() {
			break
		}
		curNS = curNS.Parent()
		depth++
	}

	// Update the labels in the forest so that they can be used in object propagation. If they've
	// changed, return true so that all propagated objects in this namespace can be compared to its
	// new labels.
	if ns.SetLabels(nsInst.Labels) {
		log.Info("Namespace's managed and/or tree labels have been updated")
		return true
	}
	return false
}

// Sync namespace managed annotations. This is mainly a simplified version of syncLabels with all
// the tree stuff taken out.
func (r *Reconciler) syncAnnotations(log logr.Logger, inst *api.HierarchyConfiguration, nsInst *corev1.Namespace, ns *forest.Namespace) {
	// Get a list of all managed annotations from the config object.
	managed := map[string]string{}
	for _, kvp := range inst.Spec.Annotations {
		if !config.IsManagedAnnotation(kvp.Key) {
			log.Info("Illegal managed annotation", "key", kvp.Key)
			ns.SetCondition(api.ConditionBadConfiguration, api.ReasonIllegalManagedAnnotation, "Not a legal managed annotation (set via --managed-namespace-annotation): "+kvp.Key)
			continue
		}
		managed[kvp.Key] = kvp.Value
	}

	// External namespaces can also have managed annotations set directly. All other namespaces must
	// have these removed now.
	for k, v := range nsInst.Annotations {
		if config.IsManagedAnnotation(k) {
			if ns.IsExternal() {
				managed[k] = v
			} else {
				delete(nsInst.Annotations, k)
			}
		}
	}

	// Record all managed annotations in the forest. If they've changed, we need to enqueue all
	// descendants to propagate the changes.
	if !reflect.DeepEqual(ns.ManagedAnnotations, managed) {
		ns.ManagedAnnotations = managed
		r.enqueueAffected(log, "managed annotations have changed", ns.DescendantNames()...)
	}

	// For external namespaces, we're done since HNC mainly doesn't manage the metadata of
	// external namespaces.
	if ns.IsExternal() {
		return
	}

	// Set all managed annotations, starting from the current namespace and going up through the
	// hierarchy.
	curNS := ns
	depth := 0
	for curNS != nil {
		// Add any managed annotations. TODO: add conditions for conflicts.
		for k, v := range curNS.ManagedAnnotations {
			metadata.SetAnnotation(nsInst, k, v)
		}

		// Stop if this namespace is halted, which could indicate a cycle or orphan.
		if curNS.IsHalted() {
			break
		}
		curNS = curNS.Parent()
		depth++
	}
}

func (r *Reconciler) syncConditions(log logr.Logger, inst *api.HierarchyConfiguration, ns *forest.Namespace, wasHalted bool) {
	// If the halted status has changed, notify
	if ns.IsHalted() != wasHalted {
		msg := ""
		if wasHalted {
			log.Info("ActivitiesHalted condition removed")
			msg = "removed"
		} else {
			log.Info("Setting ActivitiesHalted on namespace", "conditions", ns.Conditions())
			msg = "added"
		}
		r.enqueueAffected(log, "descendant of a namespace with ActivitiesHalted "+msg, ns.DescendantNames()...)
	}

	// Convert and pass in-memory conditions to HierarchyConfiguration object.
	inst.Status.Conditions = ns.Conditions()
}

// enqueueAffected enqueues all affected namespaces for later reconciliation. This occurs in a
// goroutine so the caller doesn't block; since the reconciler is never garbage-collected, this is
// safe.
//
// It's fine to call this function with `foo.Name()` even if `foo` is nil; it will just be ignored.
func (r *Reconciler) enqueueAffected(log logr.Logger, reason string, affected ...string) {
	go func() {
		for _, nm := range affected {
			// Ignore any nil namespaces (lets callers skip a nil check)
			if nm == (*forest.Namespace)(nil).Name() {
				continue
			}
			log.V(1).Info("Enqueuing for reconcilation", "affected", nm, "reason", reason)
			// The watch handler doesn't care about anything except the metadata.
			inst := &api.HierarchyConfiguration{}
			inst.ObjectMeta.Name = api.Singleton
			inst.ObjectMeta.Namespace = nm
			r.Affected <- event.GenericEvent{Object: inst}
		}
	}()
}

func (r *Reconciler) writeInstances(ctx context.Context, log logr.Logger, oldHC, newHC *api.HierarchyConfiguration, oldNS, newNS *corev1.Namespace) (bool, error) {
	isDeletingNS := !newNS.DeletionTimestamp.IsZero()
	updated := false
	if up, err := r.writeHierarchy(ctx, log, oldHC, newHC, isDeletingNS); err != nil {
		return false, err
	} else {
		updated = updated || up
	}

	if up, err := r.writeNamespace(ctx, log, oldNS, newNS); err != nil {
		return false, err
	} else {
		updated = updated || up
	}
	return updated, nil
}

func (r *Reconciler) writeHierarchy(ctx context.Context, log logr.Logger, orig, inst *api.HierarchyConfiguration, isDeletingNS bool) (bool, error) {
	if reflect.DeepEqual(orig, inst) {
		return false, nil
	}
	exists := !inst.CreationTimestamp.IsZero()
	if !exists && isDeletingNS {
		log.Info("Will not create hierarchyconfiguration since namespace is being deleted")
		return false, nil
	}

	stats.WriteHierConfig()
	if !exists {
		log.Info("Creating hierarchyconfiguration", "conditions", len(inst.Status.Conditions))
		if err := r.Create(ctx, inst); err != nil {
			log.Error(err, "while creating on apiserver")
			return false, err
		}
	} else {
		log.V(1).Info("Updating singleton on apiserver", "conditions", len(inst.Status.Conditions))
		if err := r.Update(ctx, inst); err != nil {
			log.Error(err, "while updating apiserver")
			return false, err
		}
	}

	return true, nil
}

func (r *Reconciler) writeNamespace(ctx context.Context, log logr.Logger, orig, inst *corev1.Namespace) (bool, error) {
	if reflect.DeepEqual(orig, inst) {
		return false, nil
	}

	// NB: HCR can't create namespaces, that's only in anchor reconciler
	stats.WriteNamespace()
	log.V(1).Info("Updating namespace on apiserver")
	if err := r.Update(ctx, inst); err != nil {
		log.Error(err, "while updating apiserver")
		return false, err
	}

	return true, nil
}

// updateObjects calls all type reconcillers in this namespace.
func (r *Reconciler) updateObjects(ctx context.Context, log logr.Logger, ns string) error {
	log.V(1).Info("Syncing all objects (hierarchy updated or new namespace found)")
	// Use mutex to guard the read from the types list of the forest to prevent the ConfigReconciler
	// from modifying the list at the same time.
	r.Forest.Lock()
	trs := r.Forest.GetTypeSyncers()
	r.Forest.Unlock()
	for _, tr := range trs {
		if err := tr.SyncNamespace(ctx, log, ns); err != nil {
			return err
		}
	}

	return nil
}

// getSingleton returns the singleton if it exists, or creates an empty one if
// it doesn't. The second parameter is true if the CRD itself is being deleted.
func (r *Reconciler) getSingleton(ctx context.Context, nm string) (*api.HierarchyConfiguration, bool, error) {
	nnm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
	inst := &api.HierarchyConfiguration{}
	if err := r.Get(ctx, nnm, inst); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, err
		}

		// It doesn't exist - initialize it to a sane initial value.
		inst.ObjectMeta.Name = api.Singleton
		inst.ObjectMeta.Namespace = nm
	}

	// If the HC is either being deleted, or it doesn't exist, this may be because HNC is being
	// uninstalled and the HierarchyConfiguration CRD is being/has been deleted. If so, we'll need to
	// put a critical condition on this singleton so that we stop making any changes to its objects,
	// but we can't just stop syncing it because we may need to delete its finalizers (see #824).
	deletingCRD := false
	if inst.CreationTimestamp.IsZero() || !inst.DeletionTimestamp.IsZero() {
		var err error
		deletingCRD, err = crd.IsDeletingCRD(ctx, api.HierarchyConfigurations)
		if err != nil {
			return nil, false, err
		}
	}

	return inst, deletingCRD, nil
}

// getNamespace returns the namespace if it exists, or returns an invalid, blank, unnamed one if it
// doesn't. This allows it to be trivially identified as a namespace that doesn't exist, and also
// allows us to easily modify it if we want to create it.
func (r *Reconciler) getNamespace(ctx context.Context, nm string) (*corev1.Namespace, error) {
	ns := &corev1.Namespace{}
	nnm := types.NamespacedName{Name: nm}
	if err := r.Get(ctx, nnm, ns); err != nil {
		return nil, err
	}
	return ns, nil
}

// getAnchorNames returns a list of anchor names in the given namespace.
func (r *Reconciler) getAnchorNames(ctx context.Context, nm string) ([]string, error) {
	var anms []string

	// List all the anchor in the namespace.
	anchorList := &api.SubnamespaceAnchorList{}
	if err := r.List(ctx, anchorList, client.InNamespace(nm)); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		return anms, nil
	}

	// Create a list of strings of the anchor names.
	for _, inst := range anchorList.Items {
		anms = append(anms, inst.GetName())
	}

	return anms, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, maxReconciles int) error {
	// Maps namespaces to their singletons
	nsMapFn := func(obj client.Object) []reconcile.Request {
		return []reconcile.Request{
			{NamespacedName: types.NamespacedName{
				Name:      api.Singleton,
				Namespace: obj.GetName(),
			}},
		}
	}
	// Maps a subnamespace anchor to the parent singleton.
	// Also maps a subnamespace anchor to the child singleton.
	// Required as the anchor needs to be reconciled to add or remove finalizers
	// The subnamespace created by the anchor needs to be reconciled in case managed labels or annotations have changed
	anchorMapFn := func(obj client.Object) []reconcile.Request {
		return []reconcile.Request{
			{NamespacedName: types.NamespacedName{
				Name:      api.Singleton,
				Namespace: obj.GetNamespace(),
			}},
			{NamespacedName: types.NamespacedName{
				Name:      api.Singleton,
				Namespace: obj.GetName(),
			}},
		}
	}
	opts := controller.Options{
		MaxConcurrentReconciles: maxReconciles,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.HierarchyConfiguration{}).
		Watches(&source.Channel{Source: r.Affected}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Namespace{}}, handler.EnqueueRequestsFromMapFunc(nsMapFn)).
		Watches(&source.Kind{Type: &api.SubnamespaceAnchor{}}, handler.EnqueueRequestsFromMapFunc(anchorMapFn)).
		WithOptions(opts).
		Complete(r)
}

func (r *Reconciler) getSubnamespaceAnchorInstance(ctx context.Context, pnm, nm string) (*api.SubnamespaceAnchor, error) {
	nsn := types.NamespacedName{Namespace: pnm, Name: nm}
	inst := &api.SubnamespaceAnchor{}
	if err := r.Get(ctx, nsn, inst); err != nil {
		return nil, err
	}
	return inst, nil
}
