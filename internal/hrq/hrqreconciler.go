package hrq

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1 "k8s.io/api/core/v1"
	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/hrq/utils"
	"sigs.k8s.io/hierarchical-namespaces/internal/logutils"
)

// RQEnqueuer enqueues ResourceQuota objects in a namespace and its descendants.
// ResourceQuotaReconciler implements the interface so that it can be called by
// the HierarchicalResourceQuotaReconciler when HierarchicalResourceQuota objects
// change.
type RQEnqueuer interface {
	// EnqueueSubtree enqueues ResourceQuota objects in a namespace and its descendants. It's used by
	// the HRQ reconciler when an HRQ has been updated and all the RQs that implement it need to be
	// updated too.
	EnqueueSubtree(log logr.Logger, nsnm string)
}

// HierarchicalResourceQuotaReconciler reconciles a HierarchicalResourceQuota object. It has three key
// purposes:
//  1. Update the in-memory forest with all the limits defined in the HRQ spec, so that they can be
//     used during admission control to reject requests.
//  2. Write all the usages from the in-memory forest back to the HRQ status.
//  3. Enqueue all relevant RQs when an HRQ changes.
type HierarchicalResourceQuotaReconciler struct {
	client.Client
	Log logr.Logger

	// Forest is the in-memory data structure that is shared with all other reconcilers.
	Forest *forest.Forest
	// trigger is a channel of event.GenericEvent (see "Watching Channels" in
	// https://book-v1.book.kubebuilder.io/beyond_basics/controller_watches.html)
	// that is used to enqueue the singleton to trigger reconciliation.
	trigger chan event.GenericEvent
	// RQEnqueuer enqueues ResourceQuota objects when HierarchicalResourceQuota
	// objects change.
	RQR RQEnqueuer
}

// +kubebuilder:rbac:groups=hnc.x-k8s.io,resources=hierarchicalresourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hnc.x-k8s.io,resources=hierarchicalresourcequotas/status,verbs=get;update;patch

func (r *HierarchicalResourceQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logutils.WithRID(r.Log).WithValues("trigger", req.NamespacedName)

	inst := &api.HierarchicalResourceQuota{}
	err := r.Get(ctx, req.NamespacedName, inst)
	if err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Couldn't read the object")
		return ctrl.Result{}, err
	}

	// If an object is deleted, assign the name and namespace of the request to
	// the object so that they can be used to sync with the forest.
	if isDeleted(inst) {
		inst.ObjectMeta.Name = req.NamespacedName.Name
		inst.ObjectMeta.Namespace = req.NamespacedName.Namespace
	}
	oldUsages := inst.Status.Used

	// Sync with forest to update the HRQ or the in-memory forest.
	updatedInst, updatedForest := r.syncWithForest(inst)

	// If an object is deleted, we will not write anything back to the api server.
	if !isDeleted(inst) && updatedInst {
		log.V(1).Info("Updating HRQ", "oldUsages", oldUsages, "newUsages", inst.Status.Used, "limits", inst.Spec.Hard)
		if err := r.Update(ctx, inst); err != nil {
			log.Error(err, "Couldn't write the object")
			return ctrl.Result{}, err
		}
	}

	// Enqueue ResourceQuota objects in the current namespace and its descendants
	// if hard limits of the HRQ object are changed or first-time synced. This
	// includes the case when the object is deleted and all its hard limits are
	// removed as a result.
	if updatedForest {
		log.Info("HRQ hard limits updated", "limits", inst.Spec.Hard)
		reason := fmt.Sprintf("Updated hard limits in subtree %q", inst.GetNamespace())
		r.RQR.EnqueueSubtree(log.WithValues("reason", reason), inst.GetNamespace())
	}

	return ctrl.Result{}, nil
}

// syncWithForest syncs resource limits and resource usages with the in-memory
// forest. The first return value is true if the HRQ object is updated; the
// second return value is true if the forest is updated.
func (r *HierarchicalResourceQuotaReconciler) syncWithForest(inst *api.HierarchicalResourceQuota) (bool, bool) {
	r.Forest.Lock()
	defer r.Forest.Unlock()

	updatedInst := false
	// Update HRQ limits if they are different in the spec and status.
	if !utils.Equals(inst.Spec.Hard, inst.Status.Hard) {
		inst.Status.Hard = inst.Spec.Hard
		updatedInst = true
	}
	oldUsages := inst.Status.Used

	// Update the forest if the HRQ limits are changed or first-time synced.
	updatedForest := r.syncLimits(inst)

	// Update HRQ usages if they are changed.
	r.syncUsages(inst)
	updatedInst = updatedInst || !utils.Equals(oldUsages, inst.Status.Used)

	return updatedInst, updatedForest
}

// syncLimits syncs in-memory resource limits with the limits specified in the
// spec. Returns true if there's a difference.
func (r *HierarchicalResourceQuotaReconciler) syncLimits(inst *api.HierarchicalResourceQuota) bool {
	ns := r.Forest.Get(inst.GetNamespace())
	if isDeleted(inst) {
		ns.RemoveLimits(inst.GetName())
		return true
	}
	return ns.UpdateLimits(inst.GetName(), inst.Spec.Hard)
}

// syncUsages updates resource usage status based on in-memory resource usages.
func (r *HierarchicalResourceQuotaReconciler) syncUsages(inst *api.HierarchicalResourceQuota) {
	// If the object is deleted, there is no need to update its usage status.
	if isDeleted(inst) {
		return
	}
	ns := r.Forest.Get(inst.GetNamespace())

	// Filter the usages to only include the resource types being limited by this HRQ and write those
	// usages back to the HRQ status.
	inst.Status.Used = utils.FilterUnlimited(ns.GetSubtreeUsages(), inst.Spec.Hard)

	// Update status.request and status.limit to show HRQ status by using kubectl get
	resources := make([]v1.ResourceName, 0, len(inst.Status.Hard))
	for resource := range inst.Status.Hard {
		resources = append(resources, resource)
	}
	sort.Sort(sortableResourceNames(resources))

	requestColumn := bytes.NewBuffer([]byte{})
	limitColumn := bytes.NewBuffer([]byte{})
	for i := range resources {
		w := requestColumn
		resource := resources[i]
		usedQuantity := inst.Status.Used[resource]
		hardQuantity := inst.Status.Hard[resource]

		// use limitColumn writer if a resource name prefixed with "limits" is found
		if pieces := strings.Split(resource.String(), "."); len(pieces) > 1 && pieces[0] == "limits" {
			w = limitColumn
		}

		fmt.Fprintf(w, "%s: %s/%s, ", resource, usedQuantity.String(), hardQuantity.String())
	}

	inst.Status.RequestsSummary = strings.TrimSuffix(requestColumn.String(), ", ")
	inst.Status.LimitsSummary = strings.TrimSuffix(limitColumn.String(), ", ")

}

func isDeleted(inst *api.HierarchicalResourceQuota) bool {
	return inst.GetCreationTimestamp() == (metav1.Time{})
}

// allSubtreeHRQs returns a slice of all HRQ objects in a subtree
func (r *HierarchicalResourceQuotaReconciler) allSubtreeHRQs(ns *forest.Namespace) []api.HierarchicalResourceQuota {
	insts := []api.HierarchicalResourceQuota{}
	for _, nsnm := range ns.AncestryNames() {
		for _, hrqnm := range r.Forest.Get(nsnm).HRQNames() {
			inst := api.HierarchicalResourceQuota{}
			inst.ObjectMeta.Name = hrqnm
			inst.ObjectMeta.Namespace = nsnm

			insts = append(insts, inst)
		}
	}
	return insts
}

// OnChangeNamespace enqueues all HRQ objects in the subtree for later reconciliation.
// This is needed so that the HRQ objects are enqueued for reconciliation when there is a
// change in the tree hierarchy which affects the subtree usage of the HRQ objects.
// This occurs in a goroutine so the caller doesn't block; since the
// reconciler is never garbage-collected, this is safe.
func (r *HierarchicalResourceQuotaReconciler) OnChangeNamespace(log logr.Logger, ns *forest.Namespace) {
	insts := r.allSubtreeHRQs(ns)
	go func() {
		for _, inst := range insts {
			r.trigger <- event.GenericEvent{Object: &inst}
		}
	}()
}

// Enqueue enqueues a specific HierarchicalResourceQuota object to trigger the reconciliation of the
// object for a given reason. This occurs in a goroutine so the caller doesn't block; since the
// reconciler is never garbage-collected, this is safe.
//
// It's called by the RQ reconciler when an RQ's status has changed, which might indicate that the
// HRQ's status (specifically its usage) needs to be changed as well.
func (r *HierarchicalResourceQuotaReconciler) Enqueue(log logr.Logger, reason, ns, nm string) {
	go func() {
		log.V(1).Info("Enqueuing for reconciliation", "reason", reason, "enqueuedName", nm, "enqueuedNamespace", ns)
		// The watch handler doesn't care about anything except the metadata.
		inst := &api.HierarchicalResourceQuota{}
		inst.ObjectMeta.Name = nm
		inst.ObjectMeta.Namespace = ns
		r.trigger <- event.GenericEvent{Object: inst}
	}()
}

func (r *HierarchicalResourceQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.trigger = make(chan event.GenericEvent)
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.HierarchicalResourceQuota{}).
		Watches(&source.Channel{Source: r.trigger}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}

// sortableResourceNames - An array of sortable resource names
type sortableResourceNames []v1.ResourceName

func (list sortableResourceNames) Len() int {
	return len(list)
}

func (list sortableResourceNames) Swap(i, j int) {
	list[i], list[j] = list[j], list[i]
}

func (list sortableResourceNames) Less(i, j int) bool {
	return list[i] < list[j]
}
