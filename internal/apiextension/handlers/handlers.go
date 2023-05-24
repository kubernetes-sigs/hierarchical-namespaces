// Package handlers provides HTTP handlers for the resource list API extension.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	corecache "k8s.io/client-go/listers/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/apiextension/apiresources"
	"sigs.k8s.io/hierarchical-namespaces/internal/apiextension/clients"
)

const (
	allowedCNKey   = "requestheader-allowed-names"
	workers        = 3
	hnsLabelSuffix = ".tree.hnc.x-k8s.io/depth"
)

var (
	paramScheme = runtime.NewScheme()
	paramCodec  = runtime.NewParameterCodec(paramScheme)
	log         = zap.New().WithName("apiextension").WithName("handlers")
)

func init() {
	metav1.AddToGroupVersion(paramScheme, metav1.SchemeGroupVersion)
}

// AuthenticateMiddleware returns a middleware handler that checks that the incoming request's client certificate CN matches an allowlist of names.
// See https://kubernetes.io/docs/tasks/extend-kubernetes/configure-aggregation-layer/#kubernetes-apiserver-client-authentication
// for more information.
// The certificate's validity (requestheader-client-ca-file) is checked as part of the HTTP server connection.
func AuthenticateMiddleware(configMapCache corecache.ConfigMapNamespaceLister, configMapName string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.TLS.PeerCertificates) == 0 {
				log.Info("user is not authenticated")
				http.Error(w, "user is not authenticated", http.StatusUnauthorized)
				return
			}
			requestCN := r.TLS.PeerCertificates[0].Subject.CommonName
			log.V(5).Info("authenticating user %s", requestCN)
			config, err := configMapCache.Get(configMapName)
			if err != nil {
				log.Error(err, "could not authenticate API server")
				http.Error(w, fmt.Sprintf("could not authenticate API server, error: %v", err), http.StatusInternalServerError)
				return
			}
			allowedCNString, ok := config.Data[allowedCNKey]
			if !ok {
				http.Error(w, "could not authenticate API server, invalid extension config", http.StatusInternalServerError)
			}
			allowedCN := []string{}
			if err := json.Unmarshal([]byte(allowedCNString), &allowedCN); err != nil {
				log.Error(err, "could not authenticate API server")
				http.Error(w, fmt.Sprintf("could not authenticate API server, error: %v", err), http.StatusInternalServerError)
				return
			}
			found := false
			for _, allowed := range allowedCN {
				if allowed == requestCN {
					found = true
					break
				}
			}
			if !found {
				log.Info("could not find user %s in allowed users", requestCN)
				http.Error(w, fmt.Sprintf("user %s not allowed", requestCN), http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// DiscoveryHandler returns an HTTP handler that lists all available resource types for this API.
// This allows the Kubernetes API server to register every resource as a native type and makes them
// appear in `kubectl api-resources`.
func DiscoveryHandler(apis apiresources.APIResourceWatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		log.V(5).Info("handling request %s", req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"kind":         "APIResourceList",
			"apiVersion":   "v1",
			"groupVersion": api.ResourcesGroupVersion.String(),
			"resources":    apis.List(),
		}
		apiResourceBytes, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(apiResourceBytes)
	}
}

// Forwarder returns an HTTP handler that handles requests for namespaced resources that have no particular namespace specified
// (e.g., kubectl get pods -A). The result is the equivalent of calling list or watch on the original endpoint.
// It is included for completeness, but clients should generally use the regular API for the resource.
func Forwarder(clientGetter clients.ClientGetter, apis apiresources.APIResourceWatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.V(5).Info("handling request %s", r.URL.Path)
		vars := mux.Vars(r)
		resource, err := gvrFromVars(vars, apis)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		resourceClient, err := clientGetter(r, resource)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		opts := metav1.ListOptions{}
		paramCodec.DecodeParameters(r.URL.Query(), metav1.SchemeGroupVersion, &opts)

		if opts.Watch {
			watcher, err := getWatchers(r.Context(), resourceClient, nil, opts)
			if isErrorAndHandleError(w, err) {
				return
			}
			watchHandler(w, r, resourceClient, watcher, opts)
			return
		}
		resources, err := resourceClient.List(r.Context(), opts)
		if isErrorAndHandleError(w, err) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		returnResp(w, resources.UnstructuredContent())
	}
}

// NamespaceHandler returns an HTTP handler that handles requests for namespaced resources either in the given namespace
// or in a namespace descended from the given namespace.
func NamespaceHandler(clientGetter clients.ClientGetter, apis apiresources.APIResourceWatcher, namespaceCache corecache.NamespaceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.V(5).Info("handling request %s", r.URL.Path)

		vars := mux.Vars(r)
		namespace := vars["namespace"]
		resource, err := gvrFromVars(vars, apis)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		resourceClient, err := clientGetter(r, resource)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		opts := metav1.ListOptions{}
		paramCodec.DecodeParameters(r.URL.Query(), metav1.SchemeGroupVersion, &opts)

		label := namespace + hnsLabelSuffix
		selector, err := labels.Parse(label)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		namespaces, err := namespaceCache.List(selector)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if opts.Watch {
			watchers, err := getWatchers(r.Context(), resourceClient, namespaces, opts)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			watchHandler(w, r, resourceClient, watchers, opts)
			return
		}
		listHandler(w, r, resource, resourceClient, namespaces, opts, apis)
	}
}

func getWatchers(ctx context.Context, client dynamic.NamespaceableResourceInterface, namespaces []*corev1.Namespace, opts metav1.ListOptions) ([]watch.Interface, error) {
	if len(namespaces) == 0 {
		watcher, err := client.Watch(ctx, opts)
		if err != nil {
			return nil, err
		}
		return []watch.Interface{watcher}, nil
	}
	watchers := make([]watch.Interface, 0)
	for _, ns := range namespaces {
		watcher, err := client.Namespace(ns.Name).Watch(ctx, opts)
		if err != nil {
			return nil, err
		}
		watchers = append(watchers, watcher)
	}
	return watchers, nil
}

func watchHandler(w http.ResponseWriter, r *http.Request, client dynamic.NamespaceableResourceInterface, watchers []watch.Interface, opts metav1.ListOptions) {
	events := make(chan metav1.WatchEvent)
	doneEvents := make(chan bool)

	go func() {
		w.Header().Set("Content-Type", "application/json")
		for event := range events {
			returnResp(w, event)
			w.(http.Flusher).Flush()
		}
		doneEvents <- true
	}()

	eg, ctx := errgroup.WithContext(r.Context())
	for _, watcher := range watchers {
		watcher := watcher
		eg.Go(func() error {
			for {
				select {
				case event, ok := <-watcher.ResultChan():
					if !ok {
						log.V(5).Info("received empty event, stopping watch")
						return nil
					}
					outEvent, err := convertEvent(event)
					if err != nil {
						return err
					}
					events <- *outEvent
				case <-ctx.Done():
					return nil
				}
			}
		})
	}
	err := eg.Wait()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	if ctx.Err() == context.Canceled {
		log.V(4).Info("client disconnected: %v", r.RemoteAddr)
	}
	close(events)
	<-doneEvents
}

func listHandler(w http.ResponseWriter, r *http.Request, resource schema.GroupVersionResource, client dynamic.NamespaceableResourceInterface, namespaces []*corev1.Namespace, opts metav1.ListOptions, apis apiresources.APIResourceWatcher) {
	itemsChan := make(chan unstructured.Unstructured)
	rowsChan := make(chan interface{})
	itemsList := make([]unstructured.Unstructured, 0)
	rowList := make([]interface{}, 0)

	latestResourceVersion := 0
	resourceVersions := make(chan int)
	var columns []interface{}

	wg := sync.WaitGroup{}
	wg.Add(3)

	go func() {
		for i := range itemsChan {
			itemsList = append(itemsList, i)
		}
		wg.Done()
	}()

	go func() {
		for r := range rowsChan {
			rowList = append(rowList, r)
		}
		wg.Done()
	}()

	go func() {
		for r := range resourceVersions {
			if r > latestResourceVersion {
				latestResourceVersion = r
			}
		}
		wg.Done()
	}()

	eg, ctx := errgroup.WithContext(r.Context())
	sem := semaphore.NewWeighted(workers)
	for _, ns := range namespaces {
		ns := ns.Name
		if err := sem.Acquire(ctx, 1); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		eg.Go(func() error {
			defer sem.Release(1)
			resourcesForNamespace, err := client.Namespace(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			if resourcesForNamespace == nil {
				return nil
			}
			rows, _ := resourcesForNamespace.Object["rows"].([]interface{})
			columns, _ = resourcesForNamespace.Object["columnDefinitions"].([]interface{})
			if len(resourcesForNamespace.Items) > 0 || (len(rows) > 0) {
				// resourceVersion will be different for every request, and in the end we want the latest one,
				// but we won't know which one is the latest until the channel is done processing.

				rv, err := strconv.Atoi(resourcesForNamespace.GetResourceVersion())
				if err != nil {
					rv = 0
				}
				resourceVersions <- rv
			}
			for _, r := range rows {
				rowsChan <- r
			}
			for _, r := range resourcesForNamespace.Items {
				itemsChan <- r

			}

			return nil
		})
	}
	err := eg.Wait()
	if isErrorAndHandleError(w, err) {
		return
	}
	close(itemsChan)
	close(rowsChan)
	close(resourceVersions)
	wg.Wait()

	sortItems(itemsList)
	sortRows(rowList)

	resourceVersion := strconv.Itoa(latestResourceVersion)
	if resourceVersion == "0" { // this may happen if the namespace slice was empty, but we still need to return a valid resource version.
		resourceVersion, err = emptyResourceVersion(r.Context(), resource, client)
		if isErrorAndHandleError(w, err) {
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if len(rowList) > 0 {
		resp := responseTable(resourceVersion, columns, rowList)
		returnResp(w, resp)
		return
	}
	kind, err := apis.GetKindForResource(resource)
	if isErrorAndHandleError(w, err) {
		return
	}
	resp := responseData(resource, kind+"List", resourceVersion, itemsList)
	returnResp(w, resp)
}

func sortItems(items []unstructured.Unstructured) error {
	var err error
	sort.Slice(items, func(i, j int) bool {
		objI := items[i]
		objJ := items[j]
		if objI.GetNamespace() != objJ.GetNamespace() {
			return objI.GetNamespace() < objJ.GetNamespace()
		}
		return objI.GetName() < objJ.GetName()

	})
	return err
}

func sortRows(rows []interface{}) {
	sort.Slice(rows, func(i, j int) bool {
		rowI, _ := rows[i].(map[string]interface{})
		objI, _ := rowI["object"].(map[string]interface{})
		unstI := unstructured.Unstructured{Object: objI}
		rowJ, _ := rows[j].(map[string]interface{})
		objJ, _ := rowJ["object"].(map[string]interface{})
		unstJ := unstructured.Unstructured{Object: objJ}
		if unstI.GetNamespace() < unstJ.GetNamespace() {
			return true
		}
		if unstI.GetNamespace() > unstJ.GetNamespace() {
			return false
		}
		return unstI.GetName() < unstJ.GetName()
	})
}

func gvrFromVars(vars map[string]string, apis apiresources.APIResourceWatcher) (schema.GroupVersionResource, error) {
	groupResource := strings.Split(vars["resource"], ".")
	group := ""
	if len(groupResource) > 1 {
		group = strings.Join(groupResource[:len(groupResource)-1], ".")
	}
	resourceName := groupResource[len(groupResource)-1]
	resource, ok := apis.Get(resourceName, group)
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("could not find resource %s", vars["resource"])
	}
	return schema.GroupVersionResource{Group: group, Version: resource.Version, Resource: resourceName}, nil
}

func convertEvent(event watch.Event) (*metav1.WatchEvent, error) {
	internalEvent := metav1.InternalEvent(event)
	outEvent := &metav1.WatchEvent{}
	err := metav1.Convert_v1_InternalEvent_To_v1_WatchEvent(&internalEvent, outEvent, nil)
	if err != nil {
		return nil, err
	}
	return outEvent, nil
}

func returnResp(w http.ResponseWriter, resp interface{}) {
	resourceJSON, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(resourceJSON)
	w.Write([]byte("\n"))
}

func emptyResourceVersion(ctx context.Context, gvr schema.GroupVersionResource, resourceClient dynamic.ResourceInterface) (string, error) {
	// get the list but ignore the resources, this is needed to get the list resource version
	resourceList, err := resourceClient.List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return "", fmt.Errorf("failed to get resource version for resource %s: %w", gvr.Resource, err)
	}
	return resourceList.GetResourceVersion(), nil
}

func responseData(resource schema.GroupVersionResource, kind, resourceVersion string, items []unstructured.Unstructured) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": resource.GroupVersion().String(),
		"kind":       kind,
		"metadata": map[string]interface{}{
			"resourceVersion": resourceVersion,
		},
		"items": items,
	}
}

func responseTable(resourceVersion string, columnDefinitions interface{}, rows interface{}) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	list.SetUnstructuredContent(map[string]interface{}{
		"columnDefinitions": columnDefinitions,
		"rows":              rows,
	})
	list.SetAPIVersion("meta.k8s.io/v1")
	list.SetKind("Table")
	list.SetResourceVersion(resourceVersion)
	return list
}

func isErrorAndHandleError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return true
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return true
}
