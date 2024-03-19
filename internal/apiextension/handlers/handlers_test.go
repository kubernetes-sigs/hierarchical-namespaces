package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamic "k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corecache "k8s.io/client-go/listers/core/v1"
	clienttesting "k8s.io/client-go/testing"
)

func TestAuthenticateMiddleware(t *testing.T) {
	configMapName := "extension-apiserver-authentication"

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
	})

	tests := []struct {
		name           string
		tls            *tls.ConnectionState
		configMapCache corecache.ConfigMapNamespaceLister
		wantCode       int
	}{
		{
			name:     "no certificate",
			tls:      &tls.ConnectionState{},
			wantCode: http.StatusUnauthorized,
		},
		{
			name: "wrong name",
			tls: &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{
					{
						Subject: pkix.Name{
							CommonName: "wrong-name",
						},
					},
				},
			},
			configMapCache: &mockConfigMap{
				cm: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: configMapName,
					},
					Data: map[string]string{
						"requestheader-allowed-names": `["test-users"]`,
					},
				},
			},
			wantCode: http.StatusUnauthorized,
		},
		{
			name: "no configmap",
			tls: &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{
					{
						Subject: pkix.Name{
							CommonName: "test-users",
						},
					},
				},
			},
			configMapCache: &mockConfigMap{},
			wantCode:       http.StatusInternalServerError,
		},
		{
			name: "bad configmap data",
			tls: &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{
					{
						Subject: pkix.Name{
							CommonName: "test-users",
						},
					},
				},
			},
			configMapCache: &mockConfigMap{
				cm: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: configMapName,
					},
				},
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "bad configmap json",
			tls: &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{
					{
						Subject: pkix.Name{
							CommonName: "test-users",
						},
					},
				},
			},
			configMapCache: &mockConfigMap{
				cm: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: configMapName,
					},
					Data: map[string]string{
						"requestheader-allowed-names": "notarrayvalue",
					},
				},
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "right name with correct configuration",
			tls: &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{
					{
						Subject: pkix.Name{
							CommonName: "test-users",
						},
					},
				},
			},
			configMapCache: &mockConfigMap{
				cm: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: configMapName,
					},
					Data: map[string]string{
						"requestheader-allowed-names": `["test-users"]`,
					},
				},
			},
			wantCode: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/resource", nil)
			req.TLS = test.tls
			middleware := AuthenticateMiddleware(test.configMapCache, configMapName)
			middleware(next).ServeHTTP(rr, req)
			assert.Equal(t, test.wantCode, rr.Code)
		})
	}

}
func TestDiscoveryHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/apis/resources.hnc.x-k8s.io/v1alpha2", nil)

	apiResource := metav1.APIResource{
		Name:       "testresource",
		Namespaced: true,
	}
	apis := &mockAPIResourceWatcher{
		apiResources: []metav1.APIResource{
			apiResource,
		},
	}
	DiscoveryHandler(apis)(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	resp := rr.Result()
	body, err := io.ReadAll(resp.Body)
	assert.Nil(t, err)
	want := map[string]interface{}{
		"apiVersion":   "v1",
		"groupVersion": "resources.hnc.x-k8s.io/v1alpha2",
		"kind":         "APIResourceList",
		"resources":    []metav1.APIResource{apiResource},
	}
	wantJSON, _ := json.Marshal(want)
	assert.Equal(t, string(wantJSON), string(body))
}

type testSuite struct {
	scheme         *runtime.Scheme
	objs           []runtime.Object
	gvrToListKind  map[schema.GroupVersionResource]string
	apis           *mockAPIResourceWatcher
	namespaceCache *mockNamespaceCache
}

func TestForwarder(t *testing.T) {
	suite := testSuite{}
	suite.scheme = runtime.NewScheme()
	suite.objs = []runtime.Object{
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResource",
				"metadata": map[string]interface{}{
					"namespace": "testns1",
					"name":      "resource1",
				},
			},
		},
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResource",
				"metadata": map[string]interface{}{
					"namespace": "testns2",
					"name":      "resource2",
				},
			},
		},
	}
	suite.gvrToListKind = map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "testgroup",
			Version:  "testversion",
			Resource: "testresources",
		}: "TestResourceList",
	}
	suite.apis = &mockAPIResourceWatcher{
		resourceMap: map[string]metav1.APIResource{
			"testgroup.testresources": metav1.APIResource{
				Name:       "testresources",
				Version:    "testversion",
				Group:      "testgroup",
				Namespaced: true,
			},
		},
	}

	t.Run("testList", suite.testForwarderList)
	t.Run("testWatch", suite.testForwarderWatch)
}

func (s *testSuite) testForwarderList(t *testing.T) {
	clientGetter := func(_ *http.Request, resource schema.GroupVersionResource) (dynamic.NamespaceableResourceInterface, error) {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(s.scheme, s.gvrToListKind, s.objs...)
		return client.Resource(resource), nil
	}

	mux := mux.NewRouter()
	mux.HandleFunc("/{resource}", Forwarder(clientGetter, s.apis))

	tests := []struct {
		name       string
		request    string
		wantBody   interface{}
		wantError  string
		wantStatus int
	}{
		{
			name:    "resource exists",
			request: "/testgroup.testresources",
			wantBody: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResourceList",
				"metadata":   map[string]interface{}{"resourceVersion": "", "continue": ""},
				"items": []interface{}{
					map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata": map[string]interface{}{
							"name":      "resource1",
							"namespace": "testns1",
						},
					},
					map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata": map[string]interface{}{
							"name":      "resource2",
							"namespace": "testns2",
						},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "resource does not exist",
			request:    "/not.available",
			wantError:  "could not find resource not.available\n",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", test.request, nil)
			mux.ServeHTTP(rr, req)
			assert.Equal(t, test.wantStatus, rr.Code)
			resp := rr.Result()
			body, err := io.ReadAll(resp.Body)
			assert.Nil(t, err)
			if test.wantError != "" {
				assert.Equal(t, test.wantError, string(body))
				return
			}
			var unmarshalledBody any
			err = json.Unmarshal(body, &unmarshalledBody)
			assert.Nil(t, err)
			assert.Equal(t, test.wantBody, unmarshalledBody)
		})
	}
}

func (s *testSuite) testForwarderWatch(t *testing.T) {
	clientGetter := func(_ *http.Request, resource schema.GroupVersionResource) (dynamic.NamespaceableResourceInterface, error) {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(s.scheme, s.gvrToListKind, s.objs...)
		client.PrependWatchReactor("testresources", func(action clienttesting.Action) (bool, watch.Interface, error) {
			watcher := watch.NewFake()
			for _, obj := range s.objs {
				obj := obj
				go watcher.Add(obj)
			}
			go func() {
				time.Sleep(100 * time.Millisecond)
				watcher.Stop()
			}()
			return true, watcher, nil
		})
		return client.Resource(resource), nil
	}

	mux := mux.NewRouter()
	mux.HandleFunc("/{resource}", Forwarder(clientGetter, s.apis))

	tests := []struct {
		name       string
		request    string
		wantBody   map[string]map[string]interface{}
		wantError  string
		wantStatus int
	}{
		{
			name:    "watch events",
			request: "/testgroup.testresources?watch=1",
			wantBody: map[string]map[string]interface{}{
				"testns1/resource1": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata": map[string]interface{}{
							"name":      "resource1",
							"namespace": "testns1",
						},
					},
				},
				"testns2/resource2": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata": map[string]interface{}{
							"name":      "resource2",
							"namespace": "testns2",
						},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", test.request, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req = req.WithContext(ctx)
			mux.ServeHTTP(rr, req)
			assert.Equal(t, test.wantStatus, rr.Code)
			resp := rr.Result()
			body, err := io.ReadAll(resp.Body)
			assert.Nil(t, err)
			if test.wantError != "" {
				assert.Equal(t, test.wantError, string(body))
				return
			}
			var bodyLines [][]byte
			if len(body) > 0 {
				bodyLines = bytes.Split(body[:len(body)-1], []byte{'\n'})
			}
			assert.Len(t, bodyLines, len(test.wantBody))
			for _, l := range bodyLines {
				var decodedLine map[string]interface{}
				err = json.Unmarshal(l, &decodedLine)
				assert.Nil(t, err)
				meta := decodedLine["object"].(map[string]interface{})["metadata"].(map[string]interface{})
				key := fmt.Sprintf("%s/%s", meta["namespace"], meta["name"])
				assert.Equal(t, test.wantBody[key], decodedLine)
			}
		})
	}
}

func TestNamespaceHandler(t *testing.T) {
	suite := testSuite{}
	suite.scheme = runtime.NewScheme()
	suite.objs = []runtime.Object{
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResource",
				"metadata": map[string]interface{}{
					"namespace": "parent1",
					"name":      "resource1",
				},
			},
		},
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResource",
				"metadata": map[string]interface{}{
					"namespace": "parent2",
					"name":      "resource2",
				},
			},
		},
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResource",
				"metadata": map[string]interface{}{
					"namespace": "child2b",
					"name":      "resourcec2b",
				},
			},
		},
		&unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResource",
				"metadata": map[string]interface{}{
					"namespace": "grandchild2",
					"name":      "resourcegc2",
				},
			},
		},
	}
	suite.gvrToListKind = map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "testgroup",
			Version:  "testversion",
			Resource: "testresources",
		}: "TestResourceList",
	}
	suite.apis = &mockAPIResourceWatcher{
		resourceMap: map[string]metav1.APIResource{
			"testgroup.testresources": metav1.APIResource{
				Name:       "testresources",
				Version:    "testversion",
				Group:      "testgroup",
				Namespaced: true,
			},
		},
		kindMapper: map[string]string{
			"testresources": "TestResource",
		},
	}
	suite.namespaceCache = &mockNamespaceCache{
		namespaces: []*corev1.Namespace{
			// descendent namespaces
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "child2a",
					Labels: map[string]string{
						"parent2.tree.hnc.x-k8s.io/depth": "2",
						"child2a.tree.hnc.x-k8s.io/depth": "1",
						"kubernetes.io/metadata.name":     "child2a",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "child2b",
					Labels: map[string]string{
						"parent2.tree.hnc.x-k8s.io/depth": "2",
						"child2b.tree.hnc.x-k8s.io/depth": "1",
						"kubernetes.io/metadata.name":     "child2b",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "grandchild2",
					Labels: map[string]string{
						"parent2.tree.hnc.x-k8s.io/depth":     "3",
						"child2a.tree.hnc.x-k8s.io/depth":     "2",
						"grandchild2.tree.hnc.x-k8s.io/depth": "1",
						"kubernetes.io/metadata.name":         "grandchild2",
					},
				},
			},
			// root namespaces
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "parent1",
					Labels: map[string]string{
						"parent1.tree.hnc.x-k8s.io/depth": "1",
						"kubernetes.io/metadata.name":     "parent1",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "parent2",
					Labels: map[string]string{
						"parent2.tree.hnc.x-k8s.io/depth": "2",
						"kubernetes.io/metadata.name":     "parent2",
					},
				},
			},
		},
	}

	t.Run("testList", suite.testNamespacedList)
	t.Run("testWatch", suite.testNamespacedWatch)
}

func (s *testSuite) testNamespacedList(t *testing.T) {
	clientGetter := func(_ *http.Request, resource schema.GroupVersionResource) (dynamic.NamespaceableResourceInterface, error) {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(s.scheme, s.gvrToListKind, s.objs...)
		return client.Resource(resource), nil
	}

	mux := mux.NewRouter()
	mux.HandleFunc("/namespaces/{namespace}/{resource}", NamespaceHandler(clientGetter, s.apis, s.namespaceCache))

	tests := []struct {
		name       string
		request    string
		namespaces []corev1.Namespace
		wantBody   interface{}
		wantStatus int
	}{
		{
			name:    "root namespace with no descendents",
			request: "/namespaces/parent1/testgroup.testresources",
			wantBody: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResourceList",
				"metadata":   map[string]interface{}{"resourceVersion": ""},
				"items": []map[string]interface{}{
					{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resource1", "namespace": "parent1"},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:    "root namespace with two generations of descendents",
			request: "/namespaces/parent2/testgroup.testresources",
			wantBody: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResourceList",
				"metadata":   map[string]interface{}{"resourceVersion": ""},
				"items": []map[string]interface{}{
					{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resourcec2b", "namespace": "child2b"},
					},
					map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resourcegc2", "namespace": "grandchild2"},
					},
					map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resource2", "namespace": "parent2"},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:    "leaf namespace",
			request: "/namespaces/grandchild2/testgroup.testresources",
			wantBody: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResourceList",
				"metadata":   map[string]interface{}{"resourceVersion": ""},
				"items": []map[string]interface{}{
					{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resourcegc2", "namespace": "grandchild2"},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "resource does not exist",
			request:    "/namespaces/parent1/not.available",
			wantBody:   "could not find resource not.available",
			wantStatus: http.StatusNotFound,
		},
		{
			name:    "namespace does not exist",
			request: "/namespaces/notanamespace/testgroup.testresources",
			wantBody: map[string]interface{}{
				"apiVersion": "testgroup/testversion",
				"kind":       "TestResourceList",
				"metadata":   map[string]interface{}{"resourceVersion": ""},
				"items":      []map[string]interface{}{},
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", test.request, nil)
			mux.ServeHTTP(rr, req)
			assert.Equal(t, test.wantStatus, rr.Code)
			resp := rr.Result()
			body, err := io.ReadAll(resp.Body)
			assert.Nil(t, err)
			var wantString string
			switch wantBody := test.wantBody.(type) {
			case map[string]interface{}:
				wantJSON, _ := json.Marshal(wantBody)
				wantString = string(wantJSON)
			case string:
				wantString = wantBody
			}
			assert.Equal(t, wantString, strings.TrimSpace(string(body)))
		})
	}
}

func (s *testSuite) testNamespacedWatch(t *testing.T) {
	clientGetter := func(_ *http.Request, resource schema.GroupVersionResource) (dynamic.NamespaceableResourceInterface, error) {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(s.scheme, s.gvrToListKind, s.objs...)
		client.PrependWatchReactor("testresources", func(action clienttesting.Action) (bool, watch.Interface, error) {
			watcher := watch.NewFake()
			handled := false
			for _, obj := range s.objs {
				obj := obj
				if obj.(*unstructured.Unstructured).GetNamespace() == action.GetNamespace() {
					go watcher.Add(obj)
					handled = true
				}
			}
			go func() {
				time.Sleep(100 * time.Millisecond)
				watcher.Stop()
			}()
			return handled, watcher, nil
		})
		return client.Resource(resource), nil
	}

	mux := mux.NewRouter()
	mux.HandleFunc("/namespaces/{namespace}/{resource}", NamespaceHandler(clientGetter, s.apis, s.namespaceCache))
	tests := []struct {
		name       string
		request    string
		namespaces []corev1.Namespace
		wantBody   map[string]map[string]interface{}
		wantError  string
		wantStatus int
	}{
		{
			name:    "root namespace with no descendents",
			request: "/namespaces/parent1/testgroup.testresources?watch=1",
			wantBody: map[string]map[string]interface{}{
				"parent1/resource1": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata": map[string]interface{}{
							"name":      "resource1",
							"namespace": "parent1",
						},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:    "root namespace with two generations of descendents",
			request: "/namespaces/parent2/testgroup.testresources?watch=1",
			wantBody: map[string]map[string]interface{}{
				"child2b/resourcec2b": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resourcec2b", "namespace": "child2b"},
					},
				},
				"grandchild2/resourcegc2": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resourcegc2", "namespace": "grandchild2"},
					},
				},
				"parent2/resource2": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resource2", "namespace": "parent2"},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:    "leaf namespace",
			request: "/namespaces/grandchild2/testgroup.testresources?watch=1",
			wantBody: map[string]map[string]interface{}{
				"grandchild2/resourcegc2": map[string]interface{}{
					"type": "ADDED",
					"object": map[string]interface{}{
						"apiVersion": "testgroup/testversion",
						"kind":       "TestResource",
						"metadata":   map[string]interface{}{"name": "resourcegc2", "namespace": "grandchild2"},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "resource does not exist",
			request:    "/namespaces/parent1/not.available?watch=1",
			wantError:  "could not find resource not.available\n",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "namespace does not exist",
			request:    "/namespaces/notanamespace/testgroup.testresources?watch=1",
			wantBody:   map[string]map[string]interface{}{},
			wantStatus: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", test.request, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req = req.WithContext(ctx)
			mux.ServeHTTP(rr, req)
			assert.Equal(t, test.wantStatus, rr.Code)
			resp := rr.Result()
			body, err := io.ReadAll(resp.Body)
			assert.Nil(t, err)
			if test.wantError != "" {
				assert.Equal(t, test.wantError, string(body))
				return
			}
			var bodyLines [][]byte
			if len(body) > 0 {
				bodyLines = bytes.Split(body[:len(body)-1], []byte{'\n'})
			}
			assert.Len(t, bodyLines, len(test.wantBody))
			for _, l := range bodyLines {
				var decodedLine map[string]interface{}
				err = json.Unmarshal(l, &decodedLine)
				assert.Nil(t, err)
				meta := decodedLine["object"].(map[string]interface{})["metadata"].(map[string]interface{})
				key := fmt.Sprintf("%s/%s", meta["namespace"], meta["name"])
				assert.Equal(t, test.wantBody[key], decodedLine)
			}
		})
	}
}

type mockAPIResourceWatcher struct {
	resourceMap  map[string]metav1.APIResource
	kindMapper   map[string]string
	apiResources []metav1.APIResource
}

func (m *mockAPIResourceWatcher) List() []metav1.APIResource {
	return m.apiResources
}

func (m *mockAPIResourceWatcher) Get(resource, group string) (metav1.APIResource, bool) {
	key := group + "." + resource
	val, ok := m.resourceMap[key]
	return val, ok
}

func (m *mockAPIResourceWatcher) GetKindForResource(gvr schema.GroupVersionResource) (string, error) {
	return m.kindMapper[gvr.Resource], nil
}

type mockNamespaceCache struct {
	namespaces []*corev1.Namespace
}

func (m *mockNamespaceCache) Get(name string) (*corev1.Namespace, error) {
	for _, n := range m.namespaces {
		if n.Name == name {
			return n, nil
		}
	}
	return nil, &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Code: http.StatusNotFound,
		},
	}
}

func (m *mockNamespaceCache) List(selector labels.Selector) ([]*corev1.Namespace, error) {
	result := []*corev1.Namespace{}
	for _, n := range m.namespaces {
		if selector.Matches(labels.Set(n.ObjectMeta.Labels)) {
			result = append(result, n)
		}
	}
	return result, nil
}

type mockConfigMap struct {
	cm *corev1.ConfigMap
}

func (m *mockConfigMap) Get(name string) (*corev1.ConfigMap, error) {
	if m.cm != nil && name == m.cm.Name {
		return m.cm, nil
	} else {
		return nil, fmt.Errorf("not found")
	}
}

func (m *mockConfigMap) List(selector labels.Selector) ([]*corev1.ConfigMap, error) {
	panic("not implemented")
}
