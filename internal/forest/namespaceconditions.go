package forest

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	c := metav1.Condition{
		Type:    tp,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: msg,
	}
	meta.SetStatusCondition(&ns.conditions, c)
}

// ClearConditions set conditions to nil.
func (ns *Namespace) ClearConditions() {
	ns.conditions = nil
}

// Conditions returns a full list of the conditions in the namespace.
func (ns *Namespace) Conditions() []metav1.Condition {
	if root := ns.GetHaltedRoot(); root != "" && root != ns.name {
		ret := []metav1.Condition{{
			Type:   api.ConditionActivitiesHalted,
			Status: metav1.ConditionTrue,
			// TODO(adrianludwin) Review; Some callers require this field to be set - since it is mandatory in the API
			// Set time as an obviously wrong value 1970-01-01T00:00:00Z since we
			// overwrite conditions every time.
			LastTransitionTime: metav1.Unix(0, 0),
			Reason:             api.ReasonAncestor,
			Message:            fmt.Sprintf("Propagation paused in %q and its descendants due to ActivitiesHalted condition on ancestor %q", ns.name, root),
		}}
		return append(ret, ns.conditions...)
	}
	return ns.conditions
}
