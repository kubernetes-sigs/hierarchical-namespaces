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

package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Constants for types and well-known names
const (
	Singleton               = "hierarchy"
	HierarchyConfigurations = "hierarchyconfigurations"
)

// Constants for labels and annotations
const (
	MetaGroup                 = "hnc.x-k8s.io"
	LabelInheritedFrom        = MetaGroup + "/inherited-from"
	FinalizerHasSubnamespace  = MetaGroup + "/hasSubnamespace"
	LabelTreeDepthSuffix      = ".tree." + MetaGroup + "/depth"
	AnnotationManagedBy       = MetaGroup + "/managed-by"
	AnnotationPropagatePrefix = "propagate." + MetaGroup

	AnnotationSelector     = AnnotationPropagatePrefix + "/select"
	AnnotationTreeSelector = AnnotationPropagatePrefix + "/treeSelect"
	AnnotationNoneSelector = AnnotationPropagatePrefix + "/none"
	AnnotationAllSelector  = AnnotationPropagatePrefix + "/all"

	// LabelManagedByStandard will eventually replace our own managed-by annotation (we didn't know
	// about this standard label when we invented our own).
	LabelManagedByApps = "app.kubernetes.io/managed-by"

	// LabelIncludedNamespace is the label added by HNC on the namespaces that
	// should be enforced by our validators.
	LabelIncludedNamespace = MetaGroup + "/included-namespace"
)

const (
	// Condition types.
	ConditionActivitiesHalted string = "ActivitiesHalted"
	ConditionBadConfiguration string = "BadConfiguration"

	// Condition reasons. Please keep this list in alphabetical order. IF ADDING ANYTHING HERE, PLEASE
	// ALSO ADD THEM TO AllConditions, BELOW.
	ReasonAncestor                 string = "AncestorHaltActivities"
	ReasonAnchorMissing            string = "SubnamespaceAnchorMissing"
	ReasonDeletingCRD              string = "DeletingCRD"
	ReasonIllegalManagedAnnotation string = "IllegalManagedAnnotation"
	ReasonIllegalManagedLabel      string = "IllegalManagedLabel"
	ReasonIllegalParent            string = "IllegalParent"
	ReasonInCycle                  string = "InCycle"
	ReasonParentMissing            string = "ParentMissing"
)

// AllConditions have all the conditions by type and reason. Please keep this
// list in alphabetic order. This is specifically used to clear (set to 0)
// conditions in the metrics.
var AllConditions = map[string][]string{
	ConditionActivitiesHalted: {
		ReasonAncestor,
		ReasonDeletingCRD,
		ReasonIllegalManagedAnnotation,
		ReasonIllegalManagedLabel,
		ReasonIllegalParent,
		ReasonInCycle,
		ReasonParentMissing,
	},
	ConditionBadConfiguration: {
		ReasonAnchorMissing,
	},
}

const (
	// EventCannotPropagate is for events when a namespace contains an object that
	// couldn't be propagated *out* of the namespace, to one or more of its
	// descendants. If the object couldn't be propagated to *any* descendants - for
	// example, because it has a finalizer on it (HNC can't propagate objects with
	// finalizers), the error message will point to the object in this namespace.
	// Otherwise, if it couldn't be propagated to *some* descendant, the error
	// message will point to the descendant.
	EventCannotPropagate string = "CannotPropagateObject"
	// EventCannotUpdate is for events when a namespace has an object that couldn't
	// be propagated *into* this namespace - that is, it couldn't be created in
	// the first place, or it couldn't be updated. The error message will point to
	// the source namespace.
	EventCannotUpdate string = "CannotUpdateObject"
	// EventCannotGetSelector is for events when an object has annotations that cannot be
	// parsed into a valid selector
	EventCannotParseSelector string = "CannotParseSelector"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +kubebuilder:object:root=true
// +kubebuilder:storageversion

// Hierarchy is the Schema for the hierarchies API
type HierarchyConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HierarchyConfigurationSpec   `json:"spec,omitempty"`
	Status HierarchyConfigurationStatus `json:"status,omitempty"`
}

// HierarchySpec defines the desired state of Hierarchy
type HierarchyConfigurationSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Parent indicates the parent of this namespace, if any.
	Parent string `json:"parent,omitempty"`

	// AllowCascadingDeletion indicates if the subnamespaces of this namespace are
	// allowed to cascading delete.
	AllowCascadingDeletion bool `json:"allowCascadingDeletion,omitempty"`

	// Lables is a list of labels and values to apply to the current namespace and all of its
	// descendants. All label keys must match a regex specified on the command line by
	// --managed-namespace-label. A namespace cannot have a KVP that conflicts with one of its
	// ancestors.
	Labels []MetaKVP `json:"labels,omitempty"`

	// Annotations is a list of annotations and values to apply to the current namespace and all of
	// its descendants. All annotation keys must match a regex specified on the command line by
	// --managed-namespace-annotation. A namespace cannot have a KVP that conflicts with one of its
	// ancestors.
	Annotations []MetaKVP `json:"annotations,omitempty"`
}

// HierarchyStatus defines the observed state of Hierarchy
type HierarchyConfigurationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Children indicates the direct children of this namespace, if any.
	Children []string `json:"children,omitempty"`

	// Conditions describes the errors, if any.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// HierarchyList contains a list of Hierarchy
type HierarchyConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HierarchyConfiguration `json:"items"`
}

// MetaKVP represents a label or annotation
type MetaKVP struct {
	// Key is the name of the label or annotation. It must conform to the normal rules for Kubernetes
	// label/annotation keys.
	Key string `json:"key"`

	// Value is the value of the label or annotation. It must confirm to the normal rules for
	// Kubernetes label or annoation values, which are far more restrictive for labels than for
	// anntations.
	Value string `json:"value"`
}

func init() {
	SchemeBuilder.Register(&HierarchyConfiguration{}, &HierarchyConfigurationList{})
}
