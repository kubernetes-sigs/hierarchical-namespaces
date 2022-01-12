package forest

import (
	"fmt"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

// IsHalted returns true if this namespace has an ActivitiesHalted condition for any reason *other*
// than one of its ancestors also being halted.
func (ns *Namespace) IsHalted() bool {
	if ns == nil {
		return false
	}
	for _, cond := range ns.conditions {
		if cond.Type == api.ConditionActivitiesHalted {
			return true
		}
	}
	return false
}

// GetHaltedRoot returns the name of the lowest subtree that's halted for any reason *other* than a
// higher ancestor being halted. This can be the name of the current namespace, or the empty string
// if there are no halted ancestors.
func (ns *Namespace) GetHaltedRoot() string {
	if ns == nil {
		return ""
	}
	if ns.IsHalted() {
		return ns.name
	}
	return ns.Parent().GetHaltedRoot()
}

// SetCondition adds a new condition to the current condition list.
//
// Any condition with ReasonAncestor is ignored; this is added dynamically when calling
// Conditions().
func (ns *Namespace) SetCondition(tp, reason, msg string) {
	if reason == api.ReasonAncestor {
		return
	}
	ns.conditions = append(ns.conditions, api.NewCondition(tp, reason, msg))
}

// ClearConditions set conditions to nil.
func (ns *Namespace) ClearConditions() {
	ns.conditions = nil
}

// Conditions returns a full list of the conditions in the namespace.
func (ns *Namespace) Conditions() []api.Condition {
	if root := ns.GetHaltedRoot(); root != "" && root != ns.name {
		msg := fmt.Sprintf("Propagation paused in %q and its descendants due to ActivitiesHalted condition on ancestor %q", ns.name, root)
		ret := []api.Condition{api.NewCondition(api.ConditionActivitiesHalted, api.ReasonAncestor, msg)}
		return append(ret, ns.conditions...)
	}

	return ns.conditions
}
