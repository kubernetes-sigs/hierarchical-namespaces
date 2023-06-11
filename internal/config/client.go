package config

import (
	"context"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// newCachingClient is an alternative implementation of controller-runtime's
// default client for manager.Manager.
// The only difference is that this implementation sets `CacheUnstructured` to `true` to
// cache unstructured objects.
func newCachingClient(cache cache.Cache, config *rest.Config, options client.Options, uncachedObjects ...client.Object) (client.Client, error) {
	c, err := client.New(config, options)
	if err != nil {
		return nil, err
	}

	return client.NewDelegatingClient(client.NewDelegatingClientInput{
		CacheReader:       cache,
		Client:            c,
		UncachedObjects:   uncachedObjects,
		CacheUnstructured: true,
	})
}

func NewClient(readOnly bool) cluster.NewClientFunc {
	return func(cache cache.Cache, config *rest.Config, options client.Options, uncachedObjects ...client.Object) (client.Client, error) {
		c, err := newCachingClient(cache, config, options, uncachedObjects...)
		if readOnly {
			c = &readOnlyClient{client: c}
		}
		return c, err
	}
}

// readOnlyClient is a client decorator that ensures no write operations are possible.
type readOnlyClient struct {
	client client.Client
}

func (c readOnlyClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.client.Get(ctx, key, obj)
}

func (c readOnlyClient) SubResource(subResource string) client.SubResourceClient {
	return readOnlySubResourceClient{client: c.client.SubResource(subResource)}
}

func (c readOnlyClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.client.List(ctx, list, opts...)
}

func (c readOnlyClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	// Allow callers to create subject access reviews - even in read-only mode.
	// Creating a SAR will not modify state in etcd. Used to perform RBAC checks.
	if _, ok := obj.(*authzv1.SubjectAccessReview); ok {
		return c.client.Create(ctx, obj, opts...)
	}
	return nil
}

func (c readOnlyClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return nil
}

func (c readOnlyClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return nil
}

func (c readOnlyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return nil
}

func (c readOnlyClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return nil
}

func (c readOnlyClient) Status() client.StatusWriter {
	return nopStatusWriter{}
}

func (c readOnlyClient) Scheme() *runtime.Scheme {
	return c.client.Scheme()
}

func (c readOnlyClient) RESTMapper() meta.RESTMapper {
	return c.client.RESTMapper()
}

type readOnlySubResourceClient struct {
	client client.SubResourceClient
}

func (r readOnlySubResourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	return r.client.Get(ctx, obj, subResource, opts...)
}

func (r readOnlySubResourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return nil
}

func (r readOnlySubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return nil
}

func (r readOnlySubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return nil
}

type nopStatusWriter struct{}

func (w nopStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return nil
}

func (w nopStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return nil
}

func (w nopStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return nil
}
