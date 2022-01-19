package config

// UnpropgatedAnnotations is a list of annotations on objects that should _not_ be propagated by HNC.
// Much like HNC itself, other systems (such as GKE Config Sync) use annotations to "claim" an
// object - such as deleting objects it doesn't recognize. By removing these annotations on
// propgated objects, HNC ensures that other systems won't attempt to claim the same object.
//
// This value is controlled by the --unpropagated-annotation command line, which may be set multiple
// times.
var UnpropagatedAnnotations []string

// EnforcedTypesDisabled is a boolean toggle that can be used to disable the default enforced types
// "role" and "rolebindings" from being managed by HNC. If set to `true`, it's possible to remove
// both kinds of objects from the default managed types.
//
// This option is controlled by the --disable-enforced-types command line argument.
var EnforcedTypesDisabled bool
