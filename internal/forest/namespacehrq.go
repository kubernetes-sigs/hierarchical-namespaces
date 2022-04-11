package forest

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/hierarchical-namespaces/internal/hrq/utils"
)

type quotas struct {
	// limits stores the resource limits specified by the HRQs in this namespace
	limits limits

	// used stores the resource usage in the subtree rooted in this namespace
	used usage
}

// limits maps from the name of the HRQ to the limits it specifies
type limits map[string]v1.ResourceList

// usage stores local and subtree resource usages of a namespace.
type usage struct {
	// local stores resource usages in the namespace. The resource types in `local`
	// is a union of types specified in quotas.limits of the namespace and its ancestors.
	local v1.ResourceList

	// subtree stores the aggregate resource usages in the namespace and its descendants.
	// The resource types in `subtree` are the types specified in `local` usages
	// of the namespace and its descendants.
	//
	// We need to keep track of all types in `local` usages so that when adding a
	// new limit for a type that exists only in HierarchicalResourceQuota objects
	// in descendants of the current namespace, we still have aggregate resource
	// usage for the type.
	subtree v1.ResourceList
}

// TryUseResources checks resource limits in the namespace and its ancestors
// when given proposed absolute (not delta) resource usages in the namespace. If
// the proposed resource usages are the same as the current usages, of course
// it's allowed and we do nothing. If there are any changes in the usages, we
// only check to see if the proposed changing usages are allowed. If any of them
// exceed resource limits, it returns an error; otherwise, it returns nil.
// Callers of this method are responsible for updating resource usage status of
// the HierarchicalResourceQuota objects.
//
// If the proposed resource usages changes are allowed, the method also updates
// in-memory local resource usages of the current namespace and the subtree
// resource usages of its ancestors (including itself).
//
// TryUseResources is called by the HRQ admission controller to decide if a ResourceQuota.Status
// update issued by the K8s ResourceQuota admission controller is allowed. Since UseResources()
// modifies resource usages in the in-memory forest, the forest lock should be held while calling
// the method.
//
// Normally, admission controllers shouldn't assume that if they allow a change, that this change
// will actually be performed, since another admission controller can be called later and deny it.
// Uniquely for Resource Quotas, this isn't true - the K8s apiserver only attempts to update the RQ
// status when _all_ other admission controllers have passed and the resources are about to be
// consumed. In rare cases, of course, the resources may not be consumed (e.g. due to an error in
// etcd) but the apiserver runs a cleanup process that occasionally syncs up actual usage with the
// usage recorded in RQs. When the RQs are changed, we'll be updated too.
//
// Based on observations, the K8s ResourceQuota admission controller is called only
// when a resource is consumed, not when a resource is released. Therefore, in most cases,
// the proposed resource usages that the HRQ admission controller received should
// be larger than in-memory resource usages.
//
// The proposed usage can be smaller when in-memory resource usages are out of sync
// with ResourceQuota.Status.Used. In this case, TryUseResources will still update
// in-memory usages.
//
// For example, when a user deletes a pod that consumes
// 100m cpu and immediately creates another pod that consumes 1m cpu in a namespace,
// the HRQ ResourceQuota reconciler will be triggered by ResourceQuota.Status changes twice:
// 1) when pod is deleted.
// 2) when pod is created.
// The HRQ ResourceQuota reconciler will compare `local` with ResourceQuota.Status.Used
// and will update `local` (and `subtree` in namespace and its ancestors) if it
// sees `local` is different from ResourceQuota.Status.Used.
//
// The HRQ admission coontroller will be triggered once when the pod is created.
// If the HRQ admission coontroller locks the in-memory forest before step 1)
// of the HRQ ResourceQuota reconciler, the HRQ admission controller will see proposed
// usages are smaller than in-memory usages and will update in-memory usages.
// When ResourceQuota reconciler receives the lock later, it will notice the
// ResourceQuota.Status.Used is the same as in-memory usages and will not
// update in-memory usages.
func (n *Namespace) TryUseResources(rl v1.ResourceList) error {
	// The proposed resource usages are allowed if they are the same as current
	// resource usages in the namespace.
	if utils.Equals(rl, n.quotas.used.local) {
		return nil
	}

	if err := n.canUseResources(rl); err != nil {
		// At least one of the proposed usage exceeds resource limits.
		return err
	}

	// At this point we are confident that no proposed resource usage exceeds
	// resource limits because the forest lock is held by the caller of this method.
	n.UseResources(rl)
	return nil
}

// canUseResources checks if subtree resource usages exceed resource limits
// in the namespace and its ancestors if proposed resource usages were consumed.
// The method returns an error if any *changing* subtree resource usages exceed
// the corresponding limits; otherwise, it returns nil. Note: if there's no
// *change* on a subtree resource usage and it already exceeds limits, we will
// ignore it because we don't want to block other valid resource usages.
func (n *Namespace) canUseResources(u v1.ResourceList) error {
	// For each resource, delta = proposed usage - current usage.
	delta := utils.Subtract(u, n.quotas.used.local)
	delta = utils.OmitZeroQuantity(delta)

	for _, nsnm := range n.AncestryNames() {
		ns := n.forest.Get(nsnm)
		r := utils.Copy(ns.quotas.used.subtree)
		r = utils.AddIfExists(delta, r)
		allowed, nm, exceeded := checkLimits(ns.quotas.limits, r)
		if !allowed {
			// Construct the error message similar to the RQ exceeded quota error message -
			// "exceeded quota: gke-hc-hrq, requested: configmaps=1, used: configmaps=2, limited: configmaps=2"
			msg := fmt.Sprintf("exceeded hierarchical quota in namespace %q: %q", ns.name, nm)
			for _, er := range exceeded {
				rnm := er.String()
				// Get the requested, used, limited quantity of the exceeded resource.
				rq := delta[er]
				uq := ns.quotas.used.subtree[er]
				lq := ns.quotas.limits[nm][er]
				msg += fmt.Sprintf(", requested: %s=%v, used: %s=%v, limited: %s=%v",
					rnm, &rq, rnm, &uq, rnm, &lq)
			}
			return fmt.Errorf(msg)
		}
	}

	return nil
}

// UseResources sets the absolute resource usage in this namespace, and should
// be called when we're being informed of a new set of resource usage. It also
// updates the subtree usage in this namespace and all its ancestors.
//
// The callers will typically then enqueue all ancestor HRQs to update their
// usages with apiserver.
//
// UseResources can be called in the following scenarios:
//   - Called by the HRQ admission controller when a request is allowed
//   - Called by the HRQ ResourceQuota reconciler when it observes `local`
//  usages are different from ResourceQuota.Status.Used
//   - Called by the HRQ Namespace reconciler to remove `local` usages of a
//  namespace from the subtree usages of the previous ancestors of the namespace.
func (n *Namespace) UseResources(newUsage v1.ResourceList) {
	oldUsage := n.quotas.used.local
	// We only consider limited usages.
	newUsage = utils.CleanupUnneeded(newUsage, n.Limits())
	// Early exit if there's no usages change. It's safe because the forest would
	// remain unchanged and the caller would always enqueue all ancestor HRQs.
	if utils.Equals(oldUsage, newUsage) {
		return
	}
	n.quotas.used.local = newUsage

	// Determine the delta in resource usage as this now needs to be applied to each ancestor.
	delta := utils.Subtract(newUsage, oldUsage)

	// Update subtree usages in the ancestors (including itself). The incremental
	// change here is safe because there's a goroutine periodically calculating
	// subtree usages from-scratch to make sure the forest is not out-of-sync. If
	// all goes well, the periodic sync isn't needed - it's *purely* there in case
	// there's a bug.
	for _, nsnm := range n.AncestryNames() {
		ns := n.forest.Get(nsnm)

		// Get the new subtree usage and remove no longer limited usages.
		newSubUsg := utils.Add(delta, ns.quotas.used.subtree)
		ns.UpdateSubtreeUsages(newSubUsg)
	}
}

// checkLimits checks if resource usages exceed resource limits specified in
// HierarchicalResourceQuota objects of a namespace. If resource usages exceed
// resource limits in a HierarchicalResourceQuota object, it will return false, the
// name of the HierarchicalResourceQuota Object that defines the resource limit, and
// the name(s) of the resource(s) that exceed the limits; otherwise, it will return
// true.
func checkLimits(l limits, u v1.ResourceList) (bool, string, []v1.ResourceName) {
	for nm, rl := range l {
		if allowed, exceeded := utils.LessThanOrEqual(u, rl); !allowed {
			return allowed, nm, exceeded
		}
	}
	return true, "", nil
}

// HRQNames returns the names of every HRQ object in this namespace
func (n *Namespace) HRQNames() []string {
	names := []string{}
	for nm := range n.quotas.limits {
		names = append(names, nm)
	}
	return names
}

// Limits returns limits limits specified in quotas.limits of the current namespace and
// its ancestors. If there are more than one limits for a resource type, the
// most strictest limit will be returned.
func (n *Namespace) Limits() v1.ResourceList {
	rs := v1.ResourceList{}
	for _, nsnm := range n.AncestryNames() {
		ns := n.forest.Get(nsnm)
		for _, l := range ns.quotas.limits {
			rs = utils.Min(rs, l)
		}
	}

	return rs
}

// GetLocalUsages returns a copy of local resource usages.
func (n *Namespace) GetLocalUsages() v1.ResourceList {
	u := n.quotas.used.local.DeepCopy()
	return u
}

// GetSubtreeUsages returns a copy of subtree resource usages.
func (n *Namespace) GetSubtreeUsages() v1.ResourceList {
	u := n.quotas.used.subtree.DeepCopy()
	return u
}

// UpdateSubtreeUsages sets the subtree resource usages.
func (n *Namespace) UpdateSubtreeUsages(rl v1.ResourceList) {
	n.quotas.used.subtree = utils.CleanupUnneeded(rl, n.Limits())
}

// RemoveLimits removes limits specified by the HierarchicalResourceQuota object
// of the given name.
func (n *Namespace) RemoveLimits(nm string) {
	delete(n.quotas.limits, nm)
}

// UpdateLimits updates in-memory limits of the HierarchicalResourceQuota
// object of the given name. Returns true if there's a difference.
func (n *Namespace) UpdateLimits(nm string, l v1.ResourceList) bool {
	if n.quotas.limits == nil {
		n.quotas.limits = limits{}
	}
	if utils.Equals(n.quotas.limits[nm], l) {
		return false
	}
	n.quotas.limits[nm] = l
	return true
}

// DeleteUsages removes the local usages of a namespace. The usages are also
// removed from the subtree usages of the ancestors of the namespace. The caller
// should enqueue the ancestor HRQs to update the usages.
func (n *Namespace) DeleteUsages() {
	local := v1.ResourceList{}
	for r := range n.quotas.used.local {
		local[r] = resource.MustParse("0")
	}
	n.UseResources(local)
}
