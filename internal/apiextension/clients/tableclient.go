// Package clients provides a client capable of passing through the caller's Accept headers to the API server.
// The motivation for this is to allow kubectl to request a table-formatted response.
// Since the resource list API extension sits in between the client and the Kubernetes API server, it needs to forward this formatting request through.
package clients

import (
	"errors"
	"net/http"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var errUnsupportedContentType = errors.New("could not negotiate content type")

// ClientGetter returns a dynamic client with the request's Accept headers passed through.
type ClientGetter func(*http.Request, schema.GroupVersionResource) (dynamic.NamespaceableResourceInterface, error)

// endpointRestrictions implements negotiation.EndpointRestrictions.
// See https://pkg.go.dev/k8s.io/apiserver@v0.26.5/pkg/endpoints/handlers/negotiation#EndpointRestrictions
type endpointRestrictions struct{}

// AllowsMediaTypeTransform implements negotiation.EndpointRestrictions.AllowsMediaTypeTransform.
func (e endpointRestrictions) AllowsMediaTypeTransform(mimeType string, mimeSubType string, gvk *schema.GroupVersionKind) bool {
	if mimeType != "application" {
		return false
	}
	if mimeSubType != "json" {
		return false
	}
	if gvk == nil || gvk.Kind == "" || gvk.Kind == "Table" {
		return true
	}
	return false
}

// AllowsServerVersion implements negotiation.EndpointRestrictions.AllowsServerVersion.
func (e endpointRestrictions) AllowsServerVersion(string) bool {
	return true
}

// AllowsStreamSchema implements negotiation.EndpointRestrictions.AllowsStreamSchema.
func (e endpointRestrictions) AllowsStreamSchema(string) bool {
	return true
}

// MediaTypeClientGetter sets up a roundtripper for the dynamic client and returns the interface for the given resource.
// The dynamic client ignores the AcceptContentType field on the rest config, so we need to make sure it is set on
// the round tripper in order to ensure the Accept header is passed through, for example to retrieve Table-formatted data.
func MediaTypeClientGetter(restConfig *rest.Config) ClientGetter {
	return func(r *http.Request, resource schema.GroupVersionResource) (dynamic.NamespaceableResourceInterface, error) {
		cfg, err := SetMediaType(restConfig, r.Header.Get("Accept"))
		if err != nil {
			return nil, err
		}
		dynamicClient, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		return dynamicClient.Resource(resource), nil
	}
}

func SetMediaType(cfg *rest.Config, accept string) (*rest.Config, error) {
	acceptedTypes := []runtime.SerializerInfo{
		{
			MediaType:        "application/json",
			MediaTypeType:    "application",
			MediaTypeSubType: "json",
		},
	}
	mediaType, ok := negotiation.NegotiateMediaTypeOptions(accept, acceptedTypes, endpointRestrictions{})
	if !ok {
		return nil, errUnsupportedContentType
	}
	cfgCopy := rest.CopyConfig(cfg)
	setOptions := roundTripper(mediaType)
	cfgCopy.Wrap(setOptions)
	return cfgCopy, nil
}

// Inspiration for round tripper code was drawn partly from https://github.com/rancher/steve/blob/9935404019063126b6ac856db1ad56e0bc457ff8/pkg/client/factory.go#L54
type addOptions struct {
	accept string
	query  map[string]string
	next   http.RoundTripper
}

func (a *addOptions) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Accept", a.accept)
	q := r.URL.Query()
	for k, v := range a.query {
		q.Set(k, v)
	}
	r.URL.RawQuery = q.Encode()
	return a.next.RoundTrip(r)
}

func roundTripper(mediaType negotiation.MediaTypeOptions) func(http.RoundTripper) http.RoundTripper {
	accept := mediaType.Accepted.MediaType
	if mediaType.Convert != nil {
		if mediaType.Convert.Kind != "" {
			accept += ";as=" + mediaType.Convert.Kind
		}
		if mediaType.Convert.Version != "" {
			accept += ";v=" + mediaType.Convert.Version
		}
		if mediaType.Convert.Group != "" {
			accept += ";g=" + mediaType.Convert.Group
		}
	}
	return func(rt http.RoundTripper) http.RoundTripper {
		ao := addOptions{
			accept: accept,
			next:   rt,
		}
		if mediaType.Convert != nil && mediaType.Convert.Kind == "Table" {
			ao.query = map[string]string{
				"includeObject": "Object",
			}
		}
		return &ao
	}
}
