package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/mux"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corecache "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	apiregv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/apiextension/apiresources"
	"sigs.k8s.io/hierarchical-namespaces/internal/apiextension/clients"
	"sigs.k8s.io/hierarchical-namespaces/internal/apiextension/handlers"
)

const (
	kubeSystemNamespace = "kube-system"
	extensionConfigMap  = "extension-apiserver-authentication"
	clientCAKey         = "requestheader-client-ca-file"
)

var (
	setupLog      = zap.New().WithName("setup-apiext")
	listenAddress string
	certPath      string
	keyPath       string
	debug         bool
)

func main() {
	parseFlags()
	cfg, err := getConfig()
	if err != nil {
		setupLog.Error(err, "unable to get cluster config")
		os.Exit(1)
	}
	server(context.Background(), cfg)
}

func parseFlags() {
	setupLog.Info("Parsing flags")
	flag.StringVar(&listenAddress, "address", ":7443", "The address to listen on.")
	flag.StringVar(&certPath, "cert", "", "Path to the server cert.")
	flag.StringVar(&keyPath, "key", "", "Path to the server key.")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging.")
	flag.Parse()
}

func getConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("could not get kubeconfig: %w", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func getMapper(cfg *rest.Config) (meta.RESTMapper, error) {
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	// rest mapper setup inspired by https://github.com/rancher/wrangler/blob/3032665ca5611788334c8e49516014278160ebe2/pkg/clients/clients.go#L134-L135
	// which is in turn borrowed from https://ymmt2005.hatenablog.com/entry/2020/04/14/An_example_of_using_dynamic_client_of_k8s.io/client-go
	cache := memory.NewMemCacheClient(k8sClient.Discovery())
	return restmapper.NewDeferredDiscoveryRESTMapper(cache), nil
}

func server(ctx context.Context, cfg *rest.Config) {
	discovery, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "could not start watcher")
		os.Exit(1)
	}
	clientGetter := clients.MediaTypeClientGetter(cfg)
	dynamicFactory, err := getDynamicInformerFactory(cfg)
	if err != nil {
		setupLog.Error(err, "could not get dynammic client for config")
		os.Exit(1)
	}
	crdInformer, apiServiceInformer := setUpAPIInformers(dynamicFactory, ctx.Done())
	mapper, err := getMapper(cfg)
	if err != nil {
		setupLog.Error(err, "could not get REST mapper for config")
		os.Exit(1)
	}
	apis := apiresources.WatchAPIResources(ctx, discovery, crdInformer, apiServiceInformer, mapper)
	factory, err := getInformerFactory(cfg)
	if err != nil {
		setupLog.Error(err, "could not get informer factory")
		os.Exit(1)
	}
	namespaceCache, configMapCache, err := setUpInformers(factory, ctx.Done())
	if err != nil {
		setupLog.Error(err, "failed to set up informers")
		os.Exit(1)
	}
	mux := mux.NewRouter()
	pathPrefix := fmt.Sprintf("/apis/%s", api.ResourcesGroupVersion.String())
	mux.HandleFunc(pathPrefix, handlers.DiscoveryHandler(apis))
	mux.HandleFunc(fmt.Sprintf("%s/{resource}", pathPrefix), handlers.Forwarder(clientGetter, apis))
	mux.HandleFunc(fmt.Sprintf("%s/namespaces/{namespace}/{resource}", pathPrefix), handlers.NamespaceHandler(clientGetter, apis, namespaceCache))
	mux.Use(handlers.AuthenticateMiddleware(configMapCache, extensionConfigMap))

	clientCA, err := getClientCA(configMapCache)
	if err != nil {
		setupLog.Error(err, "could not get client CA from configmap")
		os.Exit(1)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM([]byte(clientCA))
	tlsConfig := &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}
	server := http.Server{
		Addr:      listenAddress,
		Handler:   mux,
		TLSConfig: tlsConfig,
	}
	setupLog.Info(fmt.Sprintf("starting server on %s", listenAddress))
	err = server.ListenAndServeTLS(certPath, keyPath)
	if err != nil {
		setupLog.Error(err, "could not start server")
		os.Exit(1)
	}
}

func getClientCA(configMapCache corecache.ConfigMapNamespaceLister) (string, error) {
	config, err := configMapCache.Get(extensionConfigMap)
	if err != nil {
		return "", err
	}
	clientCA, ok := config.Data[clientCAKey]
	if !ok {
		return "", fmt.Errorf("invalid extension config")
	}
	return string(clientCA), nil
}

func getInformerFactory(cfg *rest.Config) (informers.SharedInformerFactory, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return informers.NewSharedInformerFactory(clientset, 0), nil
}

func getDynamicInformerFactory(cfg *rest.Config) (dynamicinformer.DynamicSharedInformerFactory, error) {
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, time.Minute), nil
}

// setUpInformers returns listers (i.e. cache interfaces) for namespaces and for configmaps in the kube-system namespace.
// See https://medium.com/codex/explore-client-go-informer-patterns-4415bb5f1fbd for information on the informer factory pattern.
func setUpInformers(factory informers.SharedInformerFactory, stop <-chan struct{}) (corecache.NamespaceLister, corecache.ConfigMapNamespaceLister, error) {
	namespaceInformer := factory.Core().V1().Namespaces()
	configMapInformer := factory.Core().V1().ConfigMaps()
	go factory.Start(stop)
	if !cache.WaitForCacheSync(stop, namespaceInformer.Informer().HasSynced, configMapInformer.Informer().HasSynced) {
		return nil, nil, fmt.Errorf("cached failed to sync")
	}
	return namespaceInformer.Lister(), configMapInformer.Lister().ConfigMaps(kubeSystemNamespace), nil
}

// setUpAPIInformers returns informer objects for CRDs and APIServices, which are used for discoverying resources.
// These informer objects can be used later to add event handlers for when CRDs or APIServies are added, modified, or removed.
func setUpAPIInformers(factory dynamicinformer.DynamicSharedInformerFactory, stop <-chan struct{}) (cache.SharedIndexInformer, cache.SharedIndexInformer) {
	crdGVR := apiextv1.SchemeGroupVersion.WithResource("customresourcedefinitions")
	crdInformer := factory.ForResource(crdGVR).Informer()
	apiServiceGVR := apiregv1.SchemeGroupVersion.WithResource("apiservices")
	apiServiceInformer := factory.ForResource(apiServiceGVR).Informer()
	go factory.Start(stop)
	return crdInformer, apiServiceInformer
}
