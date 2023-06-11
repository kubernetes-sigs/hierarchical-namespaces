// This file is a modified copy of k8s.io/kubernetes/pkg/quota/v1/resources.go
package utils

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Resources maps a ResourceName to a string representation of a quantity.
type Resources map[v1.ResourceName]string

// Equals returns true if the two lists are equivalent
func Equals(a v1.ResourceList, b v1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}

	for key, value1 := range a {
		value2, found := b[key]
		if !found {
			return false
		}
		if value1.Cmp(value2) != 0 {
			return false
		}
	}

	return true
}

// LessThanOrEqual returns true if a < b for each key in b
// If false, it returns the keys in a that exceeded b
func LessThanOrEqual(a v1.ResourceList, b v1.ResourceList) (bool, []v1.ResourceName) {
	result := true
	resourceNames := []v1.ResourceName{}
	for key, value := range b {
		if other, found := a[key]; found {
			if other.Cmp(value) > 0 {
				result = false
				resourceNames = append(resourceNames, key)
			}
		}
	}
	return result, resourceNames
}

// Add returns the result of a + b for each named resource that exists in both a and b.
func AddIfExists(a v1.ResourceList, b v1.ResourceList) v1.ResourceList {
	result := v1.ResourceList{}
	for key, value := range a {
		quantity := value.DeepCopy()
		if other, found := b[key]; found {
			quantity.Add(other)
			result[key] = quantity
		}
	}
	return result
}

// Add returns the result of a + b for each named resource
func Add(a v1.ResourceList, b v1.ResourceList) v1.ResourceList {
	result := v1.ResourceList{}
	for key, value := range a {
		quantity := value.DeepCopy()
		if other, found := b[key]; found {
			quantity.Add(other)
		}
		result[key] = quantity
	}
	for key, value := range b {
		if _, found := result[key]; !found {
			result[key] = value.DeepCopy()
		}
	}
	return result
}

// Subtract returns the result of a - b for each named resource
func Subtract(a v1.ResourceList, b v1.ResourceList) v1.ResourceList {
	result := v1.ResourceList{}
	for key, value := range a {
		quantity := value.DeepCopy()
		if other, found := b[key]; found {
			quantity.Sub(other)
		}
		result[key] = quantity
	}
	for key, value := range b {
		if _, found := result[key]; !found {
			quantity := value.DeepCopy()
			quantity.Neg()
			result[key] = quantity
		}
	}
	return result
}

// OmitLTEZero returns a list omitting zero or negative quantity resources.
func OmitLTEZero(a v1.ResourceList) v1.ResourceList {
	for n, q := range a {
		if q.IsZero() {
			delete(a, n)
		} else if q.AsApproximateFloat64() <= 0 {
			delete(a, n)
		}
	}
	return a
}

// Min returns the result of Min(a, b) (i.e., smallest resource quantity) for
// each named resource. If a resource only exists in one but not all ResourceLists
// inputs, it will be returned.
func Min(a v1.ResourceList, b v1.ResourceList) v1.ResourceList {
	result := v1.ResourceList{}
	for key, value := range a {
		if other, found := b[key]; found {
			if value.Cmp(other) > 0 {
				result[key] = other.DeepCopy()
				continue
			}
		}
		result[key] = value.DeepCopy()
	}
	for key, value := range b {
		if _, found := result[key]; !found {
			result[key] = value.DeepCopy()
		}
	}
	return result
}

// FilterUnlimited cleans up the raw usages by omitting not limited usages.
func FilterUnlimited(rawUsages v1.ResourceList, limits v1.ResourceList) v1.ResourceList {
	return mask(rawUsages, resourceNames(limits))
}

// resourceNames returns a list of all resource names in the ResourceList
func resourceNames(resources v1.ResourceList) []v1.ResourceName {
	result := []v1.ResourceName{}
	for resourceName := range resources {
		result = append(result, resourceName)
	}
	return result
}

// mask returns a new resource list that only has the values with the specified names
func mask(resources v1.ResourceList, names []v1.ResourceName) v1.ResourceList {
	nameSet := toSet(names)
	result := v1.ResourceList{}
	for key, value := range resources {
		if nameSet.Has(string(key)) {
			result[key] = value.DeepCopy()
		}
	}
	return result
}

// toSet takes a list of resource names and converts to a string set
func toSet(resourceNames []v1.ResourceName) sets.Set[string] {
	result := sets.Set[string]{}
	for _, resourceName := range resourceNames {
		result.Insert(string(resourceName))
	}
	return result
}
