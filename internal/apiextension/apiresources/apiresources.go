// Package apiresources discovers namespaceable resources and provides an interface to retrieve them.
// Any CRD or APIService event triggers a new discovery request, so the cache of resource types is always up to date.
// The code for this package is inspired by https://github.com/rancher/lasso/blob/master/pkg/dynamic/gvks.go.
package apiresources

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	apidiscovery "k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

var (
	queueRefreshDelay  = 500 * time.Millisecond
	enqueueAfterPeriod = 30 * time.Second
	log                = zap.New().WithName("apiextension").WithName("apiresources")
)

// APIResourceWatcher provides access to the Kubernetes schema.
type APIResourceWatcher interface {
	List() []metav1.APIResource
	Get(resource, group string) (metav1.APIResource, bool)
	GetKindForResource(gvr schema.GroupVersionResource) (string, error)
}

type apiResourceWatcher struct {
	sync.RWMutex

	toSync       int32
	client       discovery.DiscoveryInterface
	apiResources []metav1.APIResource
	mapper       meta.RESTMapper
	resourceMap  map[string]metav1.APIResource
	retryQueue   workqueue.RateLimitingInterface
}

// WatchAPIResources creates an APIResourceWatcher object and starts watches on CRDs and APIServices,
// which prompts it to run a discovery check to get the most up to date Kubernetes schema.
func WatchAPIResources(ctx context.Context, discovery discovery.DiscoveryInterface, crds cache.SharedIndexInformer, apiServices cache.SharedIndexInformer, mapper meta.RESTMapper) APIResourceWatcher {
	a := &apiResourceWatcher{
		client:      discovery,
		mapper:      mapper,
		resourceMap: make(map[string]metav1.APIResource),
		retryQueue:  workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}

	crds.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    a.onAdd,
			UpdateFunc: a.onUpdate,
			DeleteFunc: a.onDelete,
		},
	)

	apiServices.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    a.onAdd,
			UpdateFunc: a.onUpdate,
			DeleteFunc: a.onDelete,
		},
	)

	go a.handleRetries(ctx)
	return a
}

func (a *apiResourceWatcher) onAdd(_ interface{}) {
	a.queueRefresh()
}

func (a *apiResourceWatcher) onUpdate(_, _ interface{}) {
	a.queueRefresh()
}

func (a *apiResourceWatcher) onDelete(_ interface{}) {
	a.queueRefresh()
}

func (a *apiResourceWatcher) handleRetries(ctx context.Context) {
	defer a.retryQueue.ShutDown()
	wait.Until(a.runRetrier, time.Second, ctx.Done())
}

func (a *apiResourceWatcher) runRetrier() {
	for a.next() {
	}
}

func (a *apiResourceWatcher) next() bool {
	key, stop := a.retryQueue.Get()
	if stop {
		return false
	}
	defer a.retryQueue.Forget(key)
	defer a.retryQueue.Done(key)
	a.queueRefresh()
	return true
}

// GetKindForResource returns the resource Kind given its GVR.
func (a *apiResourceWatcher) GetKindForResource(gvr schema.GroupVersionResource) (string, error) {
	a.RLock()
	defer a.RUnlock()
	gvk, err := a.mapper.KindFor(gvr)
	if err != nil {
		return "", fmt.Errorf("failed to look up kind for resource %s, %w", gvr.Resource, err)
	}
	return gvk.Kind, nil
}

// List returns all the APIResources for the hns API.
func (a *apiResourceWatcher) List() []metav1.APIResource {
	a.RLock()
	defer a.RUnlock()
	return a.apiResources
}

// Get returns an APIResource and an existence bool given a resource and group.
func (a *apiResourceWatcher) Get(resource, group string) (metav1.APIResource, bool) {
	a.RLock()
	defer a.RUnlock()
	if group == "" {
		val, ok := a.resourceMap[resource]
		return val, ok
	}
	val, ok := a.resourceMap[group+"."+resource]
	return val, ok
}

func (a *apiResourceWatcher) queueRefresh() {
	atomic.StoreInt32(&a.toSync, 1)

	go func() {
		time.Sleep(queueRefreshDelay)
		if err := a.refreshAll(); err != nil {
			log.Error(err, "failed to sync schemas, will retry")
			atomic.StoreInt32(&a.toSync, 1)
			a.retryQueue.AddAfter("", enqueueAfterPeriod)
		}
	}()
}

func (a *apiResourceWatcher) setAPIResources() error {
	resourceList, err := discovery.ServerPreferredNamespacedResources(a.client)
	if err != nil {
		return err
	}
	result := []metav1.APIResource{}
	a.Lock()
	defer a.Unlock()
	for _, resource := range resourceList {
		if resource.GroupVersion == api.ResourcesGroupVersion.String() {
			continue
		}
		gv := strings.Split(resource.GroupVersion, "/")
		group := ""
		version := ""
		if len(gv) > 1 {
			group = gv[0]
			version = gv[1]
		} else {
			version = gv[0]
		}
		for _, r := range resource.APIResources {
			name := r.Name
			if group != "" {
				name = group + "." + name
			}
			resource := metav1.APIResource{
				Name:               name,
				Group:              group,
				Version:            version,
				Kind:               r.Kind,
				Verbs:              []string{"list", "watch"},
				Namespaced:         true,
				ShortNames:         r.ShortNames,
				StorageVersionHash: apidiscovery.StorageVersionHash(group, version, r.Kind),
			}
			result = append(result, resource)
			a.resourceMap[name] = resource
		}
	}
	a.apiResources = result
	return nil
}

func (a *apiResourceWatcher) refreshAll() error {
	if !a.needToSync() {
		return nil
	}

	log.Info("Refreshing all types")
	err := a.setAPIResources()
	if err != nil {
		return err
	}

	return nil
}

func (a *apiResourceWatcher) needToSync() bool {
	old := atomic.SwapInt32(&a.toSync, 0)
	return old == 1
}
