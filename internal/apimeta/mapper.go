package apimeta

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// NotNamespacedError is returned if the mapper can find the resource, but it is not namespaced.
type NotNamespacedError struct {
	GroupResource schema.GroupResource
}

func (e *NotNamespacedError) Error() string {
	return fmt.Sprintf("resource %q is not namespaced", e.GroupResource)
}

func IsNotNamespacedError(err error) bool {
	if err == nil {
		return false
	}
	switch err.(type) {
	case *NotNamespacedError:
		return true
	default:
		return false
	}
}

func NewGroupKindMapper(c *rest.Config) (*GroupKindMapper, error) {
	restMapper, err := apiutil.NewDynamicRESTMapper(c)
	if err != nil {
		return nil, err
	}
	return &GroupKindMapper{
		RestMapper: restMapper,
	}, nil

}

// GroupKindMapper allows callers to map resources to kinds using apiserver metadata at runtime
type GroupKindMapper struct {
	RestMapper meta.RESTMapper
}

// NamespacedKindFor maps namespaced GR to GVK by using a DynamicRESTMapper
// to discover resource types at runtime. Will return an error if the resource isn't namespaced.
func (r *GroupKindMapper) NamespacedKindFor(gr schema.GroupResource) (schema.GroupVersionKind, error) {
	resource := gr.WithVersion("")
	kinds, err := r.RestMapper.KindsFor(resource)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	// Not sure if this can ever happen? RestMapper.KindsFor does not specify if no
	// matches will error out or not, so just making sure we do not get issues.
	if len(kinds) == 0 {
		return schema.GroupVersionKind{}, &meta.NoResourceMatchError{PartialResource: resource}
	}

	// KindsFor returns the list of potential kinds in priority order
	gvk := kinds[0]
	mapping, err := r.RestMapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("while attempting to determine if kind is namespaced: %w", err)
	}
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return schema.GroupVersionKind{}, &NotNamespacedError{GroupResource: gr}
	}

	return gvk, nil
}
