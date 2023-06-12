package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// HRQLabelCleanup is added to resources created by HRQ (specifically the RQ
	// singletons) for easier cleanup later by a selector.
	HRQLabelCleanup = MetaGroup + "/hrq"

	// NonPropagateAnnotation is added to RQ singletons so that they are not
	// overwritten by ancestors.
	NonPropagateAnnotation = AnnotationPropagatePrefix + "/none"

	// EventCannotWriteResourceQuota is for events when the reconcilers cannot
	// write ResourceQuota from an HRQ. Usually it means the HRQ has invalid
	// resource quota types. The error message will point to the HRQ object.
	EventCannotWriteResourceQuota string = "CannotWriteResourceQuota"
)

// HierarchicalResourceQuotaSpec defines the desired hard limits to enforce for
// a namespace and descendant namespaces
type HierarchicalResourceQuotaSpec struct {
	// Hard is the set of desired hard limits for each named resource
	// +optional
	Hard corev1.ResourceList `json:"hard,omitempty"`
}

// HierarchicalResourceQuotaStatus defines the enforced hard limits and observed
// use for a namespace and descendant namespaces
type HierarchicalResourceQuotaStatus struct {
	// Hard is the set of enforced hard limits for each named resource
	// +optional
	Hard corev1.ResourceList `json:"hard,omitempty"`
	// Used is the current observed total usage of the resource in the namespace
	// and its descendant namespaces.
	// +optional
	Used corev1.ResourceList `json:"used,omitempty"`
	// RequestsSummary is used by kubectl get hrq, and summarizes the relevant information
	// from .status.hard.requests and .status.used.requests.
	// +optional
	RequestsSummary string `json:"requestsSummary,omitempty"`
	// LimitsSummary is used by kubectl get hrq, and summarizes the relevant information
	// from .status.hard.limits and .status.used.limits.
	// +optional
	LimitsSummary string `json:"limitsSummary,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=hierarchicalresourcequotas,shortName=hrq,scope=Namespaced
// +kubebuilder:printcolumn:name="Request",type="string",JSONPath=".status.requestsSummary"
// +kubebuilder:printcolumn:name="Limit",type="string",JSONPath=".status.limitsSummary"

// HierarchicalResourceQuota sets aggregate quota restrictions enforced for a
// namespace and descendant namespaces
type HierarchicalResourceQuota struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired quota
	Spec HierarchicalResourceQuotaSpec `json:"spec,omitempty"`
	// Status defines the actual enforced quota and its current usage
	Status HierarchicalResourceQuotaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HierarchicalResourceQuotaList contains a list of HierarchicalResourceQuota
type HierarchicalResourceQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HierarchicalResourceQuota `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HierarchicalResourceQuota{}, &HierarchicalResourceQuotaList{})
}
