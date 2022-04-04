package forest

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// SetSourceObject updates or creates the source object in forest.namespace.
func (ns *Namespace) SetSourceObject(obj *unstructured.Unstructured) {
	gvk := obj.GroupVersionKind()
	name := obj.GetName()
	_, ok := ns.sourceObjects[gvk]
	if !ok {
		ns.sourceObjects[gvk] = map[string]*unstructured.Unstructured{}
	}
	ns.sourceObjects[gvk][name] = obj
}

// GetSourceObject gets a source object by name. If it doesn't exist, return nil.
func (ns *Namespace) GetSourceObject(gvk schema.GroupVersionKind, nm string) *unstructured.Unstructured {
	return ns.sourceObjects[gvk][nm]
}

// HasSourceObject returns if the namespace has a source object.
func (ns *Namespace) HasSourceObject(gvk schema.GroupVersionKind, oo string) bool {
	return ns.GetSourceObject(gvk, oo) != nil
}

// DeleteSourceObject deletes a source object by name.
func (ns *Namespace) DeleteSourceObject(gvk schema.GroupVersionKind, nm string) {
	delete(ns.sourceObjects[gvk], nm)
	// Garbage collection
	if len(ns.sourceObjects[gvk]) == 0 {
		delete(ns.sourceObjects, gvk)
	}
}

// GetSourceNames returns all source objects in the namespace.
func (ns *Namespace) GetSourceNames(gvk schema.GroupVersionKind) []types.NamespacedName {
	o := []types.NamespacedName{}
	for _, obj := range ns.sourceObjects[gvk] {
		o = append(o, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()})
	}
	return o
}

// GetNumSourceObjects returns the total number of source objects of a specific
// GVK in the namespace.
func (ns *Namespace) GetNumSourceObjects(gvk schema.GroupVersionKind) int {
	return len(ns.sourceObjects[gvk])
}

// GetAncestorSourceNames returns all source objects with the specified name
// in the ancestors (including itself) from top down. If the name is not
// specified, all the source objects in the ancestors will be returned.
func (ns *Namespace) GetAncestorSourceNames(gvk schema.GroupVersionKind, name string) []types.NamespacedName {
	// The namespace could be nil when we use this function on "ns.Parent()" to
	// get the source objects of the ancestors excluding itself without caring if
	// the "ns.Parent()" is nil.
	if ns == nil {
		return nil
	}

	// Get the source objects in the ancestors from top down.
	allNNMs := []types.NamespacedName{}
	ancs := ns.AncestryNames()
	for _, anc := range ancs {
		nnms := ns.forest.Get(anc).GetSourceNames(gvk)
		if name == "" {
			// Get all the source objects if the name is not specified.
			allNNMs = append(allNNMs, nnms...)
		} else {
			// If a name is specified, return the matching objects.
			for _, o := range nnms {
				if o.Name == name {
					allNNMs = append(allNNMs, o)
				}
			}
		}
	}

	return allNNMs
}
