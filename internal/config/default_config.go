package config

// UnpropgatedAnnotations is a list of annotations on objects that should _not_ be propagated by HNC.
// Much like HNC itself, other systems (such as GKE Config Sync) use annotations to "claim" an
// object - such as deleting objects it doesn't recognize. By removing these annotations on
// propgated objects, HNC ensures that other systems won't attempt to claim the same object.
//
// This value is controlled by the --unpropagated-annotation command line, which may be set multiple
// times.
var UnpropagatedAnnotations []string

// UnpropgatedLabels is a list of labels on objects that should _not_ be propagated by HNC.
// Much like HNC itself, other systems (such as ArgoCD) use labels to "claim" an
// object - such as deleting objects it doesn't recognize. By removing these labels on
// propgated objects, HNC ensures that other systems won't attempt to claim the same object.
//
// This value is controlled by the --unpropagated-label command line, which may be set multiple
// times.
var UnpropagatedLabels []string

// NoPropagationLabel specifies a label Key and Value which will cause an object to be excluded
// from propagation if the object defines that label with this specific value.
type NoPropagationLabel struct {
	Key   string
	Value string
}

// NoPropagationLabels is a configuration slice that contains all NoPropagationLabel labels that should
// cause objects to be ignored from propagation.
var NoPropagationLabels []NoPropagationLabel
