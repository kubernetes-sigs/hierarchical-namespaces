/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"flag"
	"net/http"
	"os"
	"strings"

	"contrib.go.opencensus.io/exporter/prometheus"
	"contrib.go.opencensus.io/exporter/stackdriver"
	"github.com/go-logr/zapr"
	corev1 "k8s.io/api/core/v1"

	// Change to use v1 when we only need to support 1.17 and higher kubernetes versions.
	stdzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	// +kubebuilder:scaffold:imports

	v1a2 "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/setup"
	"sigs.k8s.io/hierarchical-namespaces/internal/stats"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = zap.New().WithName("setup")
)

var (
	probeAddr               string
	metricsAddr             string
	enableStackdriver       bool
	maxReconciles           int
	enableLeaderElection    bool
	leaderElectionId        string
	noWebhooks              bool
	debugLogs               bool
	testLog                 bool
	internalCert            bool
	qps                     int
	webhookServerPort       int
	restartOnSecretRefresh  bool
	unpropagatedAnnotations arrayArg
	excludedNamespaces      arrayArg
	managedNamespaceLabels  arrayArg
	managedNamespaceAnnots  arrayArg
	includedNamespacesRegex string
	webhooksOnly            bool
	enableHRQ               bool
)

// init preloads some global vars before main() starts. Since this is the top-level module, I'm not
// sure what happens _between_ init() and main() but this is the way kubebuilder left things so I'm
// going to leave it alone.
func init() {
	setupLog.Info("Starting main.go:init()")
	defer setupLog.Info("Finished main.go:init()")
	_ = clientgoscheme.AddToScheme(scheme)

	_ = v1a2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apiextensions.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	parseFlags()
	metricsCleanupFn := enableMetrics()
	defer metricsCleanupFn()
	mgr := createManager()

	// Make sure certs are managed if requested. In webhooks-only mode, we don't run the manager, and
	// rely on either a controller running in a different HNC deployment, or an external tool such as
	// cert-manager.
	certsReady := make(chan struct{})
	if internalCert && !webhooksOnly {
		setupLog.Info("Starting certificate generation")
		err := setup.ManageCerts(mgr, certsReady, restartOnSecretRefresh)
		if err != nil {
			setupLog.Error(err, "unable to set up cert rotation")
			os.Exit(1)
		}
	} else {
		close(certsReady)
	}

	setupProbeEndpoints(mgr, certsReady)

	// The call to mgr.Start will never return, but the certs won't be ready until the manager starts
	// and we can't set up the webhooks without them (the webhook server runnable will try to read the
	// certs, and if those certs don't exist, the entire process will exit). So start a goroutine
	// which will wait until the certs are ready, and then create the rest of the HNC controllers.
	go startControllers(mgr, certsReady)

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func parseFlags() {
	setupLog.Info("Parsing flags")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableStackdriver, "enable-stackdriver", true, "If true, export metrics to stackdriver")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionId, "leader-election-id", "controller-leader-election-helper",
		"Leader election id determines the name of the configmap that leader election will use for holding the leader lock.")
	flag.BoolVar(&noWebhooks, "no-webhooks", false, "Disables webhooks")
	flag.BoolVar(&debugLogs, "debug-logs", false, "Shows verbose logs.")
	flag.BoolVar(&testLog, "enable-test-log", false, "Enables test log.")
	flag.BoolVar(&internalCert, "enable-internal-cert-management", false, "Enables internal cert management. See the user guide for more information.")
	flag.IntVar(&maxReconciles, "max-reconciles", 1, "Number of concurrent reconciles to perform.")
	flag.IntVar(&qps, "apiserver-qps-throttle", 50, "The maximum QPS to the API server. See the user guide for more information.")
	flag.BoolVar(&stats.SuppressObjectTags, "suppress-object-tags", true, "If true, suppresses the kinds of object metrics to reduce metric cardinality. See the user guide for more information.")
	flag.IntVar(&webhookServerPort, "webhook-server-port", 443, "The port that the webhook server serves at.")
	flag.Var(&unpropagatedAnnotations, "unpropagated-annotation", "An annotation that, if present, will be stripped out of any propagated copies of an object. May be specified multiple times, with each instance specifying one annotation. See the user guide for more information.")
	flag.Var(&excludedNamespaces, "excluded-namespace", "A namespace that, if present, will be excluded from HNC management. May be specified multiple times, with each instance specifying one namespace. See the user guide for more information.")
	flag.StringVar(&includedNamespacesRegex, "included-namespace-regex", ".*", "Namespace regular expression. Namespaces that match this regexp will be included and handle by HNC. The regex is implicitly wrapped by \"^...$\" and may only be specified once.")
	flag.BoolVar(&restartOnSecretRefresh, "cert-restart-on-secret-refresh", false, "Kills the process when secrets are refreshed so that the pod can be restarted (secrets take up to 60s to be updated by running pods)")
	flag.Var(&managedNamespaceLabels, "managed-namespace-label", "A regex indicating the labels on namespaces that are managed by HNC. These labels may only be set via the HierarchyConfiguration object. All regexes are implictly wrapped by \"^...$\". This argument can be specified multiple times. See the user guide for more information.")
	flag.Var(&managedNamespaceAnnots, "managed-namespace-annotation", "A regex indicating the annotations on namespaces that are managed by HNC. These annotations may only be set via the HierarchyConfiguration object. All regexes are implictly wrapped by \"^...$\". This argument can be specified multiple times. See the user guide for more information.")
	flag.BoolVar(&webhooksOnly, "webhooks-only", false, "Disables the controllers so HNC can be run in HA webhook mode")
	flag.BoolVar(&enableHRQ, "enable-hrq", false, "Enables hierarchical resource quotas")
	flag.Parse()

	// Assign the array args to the configuration variables after the args are parsed.
	config.UnpropagatedAnnotations = unpropagatedAnnotations
	config.SetNamespaces(includedNamespacesRegex, excludedNamespaces...)
	if err := config.SetManagedMeta(managedNamespaceLabels, managedNamespaceAnnots); err != nil {
		setupLog.Error(err, "Illegal flag values")
		os.Exit(1)
	}

	// Basic legality checks
	if webhooksOnly && noWebhooks {
		setupLog.Info("Cannot set both --webhooks-only and --no-webhooks")
		os.Exit(1)
	}
}

// enableMetrics returns a function to call from main() to export any remaining metrics when main()
// is exiting.
func enableMetrics() func() {
	var cleanupFn func()

	// Enable OpenCensus exporters to export metrics
	// to Stackdriver Monitoring.
	// Exporters use Application Default Credentials to authenticate.
	// See https://developers.google.com/identity/protocols/application-default-credentials
	// for more details.
	if enableStackdriver {
		setupLog.Info("Creating OpenCensus->Stackdriver exporter")
		sd, err := stackdriver.NewExporter(stackdriver.Options{
			// Stackdriver’s minimum stats reporting period must be >= 60 seconds.
			// https://opencensus.io/exporters/supported-exporters/go/stackdriver/
			ReportingInterval: stats.ReportingInterval,
		})
		if err == nil {
			err = sd.StartMetricsExporter()
			if err == nil {
				cleanupFn = func() {
					// Flush must be called before main() exits to ensure metrics are recorded.
					sd.Flush()
					sd.StopMetricsExporter()
				}
			}
		}
		if err != nil {
			setupLog.Error(err, "Could not create Stackdriver exporter")
		}
	}

	// Hook up OpenCensus to Prometheus.
	//
	// Creating a prom/oc exporter automatically registers the exporter with Prometheus; we can ignore
	// the returned value since it doesn't do anything anyway. See:
	// (https://github.com/census-ecosystem/opencensus-go-exporter-prometheus/blob/2b9ada237b532c09fcb0a1242469827bdb89df41/prometheus.go#L103)
	setupLog.Info("Creating Prometheus exporter")
	_, err := prometheus.NewExporter(prometheus.Options{
		Registerer: metrics.Registry, // use the controller-runtime registry to merge with all other metrics
	})
	if err != nil {
		setupLog.Error(err, "Could not create Prometheus exporter")
	}

	return cleanupFn
}

func createManager() ctrl.Manager {
	setupLog.Info("Configuring controller-manager")
	logLevel := zapcore.InfoLevel
	if debugLogs {
		logLevel = zapcore.DebugLevel
	}
	// Create a raw (upstream) zap logger that we can pass to both
	// the zap stdlib log redirect and logr.Logger shim we use for controller-runtime.
	// Stdlib is redirected at ErrorLevel since it should only log
	// if it can't return an error, like in http.Server before a handler is invoked,
	// and we expect other libraries to do the same.
	rawlog := zap.NewRaw(zap.Level(logLevel), zap.StacktraceLevel(zapcore.PanicLevel))
	stdzap.RedirectStdLogAt(rawlog, zapcore.ErrorLevel)
	log := zapr.NewLogger(rawlog)
	ctrl.SetLogger(log)
	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = float32(qps)
	// By default, Burst is about 2x QPS, but since HNC's "bursts" can last for ~minutes
	// we need to raise the QPS param to be much higher than we ordinarily would. As a
	// result, doubling this higher threshold is probably much too high, so lower it to a more
	// reasonable number.
	//
	// TODO: Better understand the behaviour of Burst, and consider making it equal to QPS if
	// it turns out to be harmful.
	cfg.Burst = int(cfg.QPS * 1.5)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		NewClient:              config.NewClient(webhooksOnly),
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       leaderElectionId,
		Port:                   webhookServerPort,
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}
	return mgr
}

// setupProbeEndpoints registers the health endpoints
func setupProbeEndpoints(mgr ctrl.Manager, certsReady chan struct{}) {
	// We can't use the default checker directly, since the checker assumes that the webhook server
	// has been started, and it will error out (and crash HNC) if the certs don't exist yet.
	// Therefore, this thin wrapper checks whether the certs are ready, and if so, bypasses the
	// controller-manager checker.
	checker := func(req *http.Request) error {
		select {
		case <-certsReady:
			return mgr.GetWebhookServer().StartedChecker()(req)
		default:
			return errors.New("HNC internal certs are not yet ready")
		}
	}
	// If we're not running the webhooks, no point checking to see if they're up.
	if noWebhooks {
		checker = healthz.Ping
	}
	if err := mgr.AddHealthzCheck("healthz", checker); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", checker); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	setupLog.Info("Probe endpoints are configured on healthz and readyz")
}

func startControllers(mgr ctrl.Manager, certsReady chan struct{}) {
	// The controllers won't work until the webhooks are operating, and those won't work until the
	// certs are all in place.
	setupLog.Info("Waiting for certificate generation to complete")
	<-certsReady
	setupLog.Info("Certs ready")

	if testLog {
		stats.StartLoggingActivity()
	}

	// Create the central in-memory data structure for HNC, since it needs to be shared among all
	// other components.
	f := forest.NewForest()

	opts := setup.Options{
		NoWebhooks:    noWebhooks,
		MaxReconciles: maxReconciles,
		HRQ:           enableHRQ,
	}
	setup.Create(setupLog, mgr, f, opts)

	setupLog.Info("All controllers started; setup complete")
}

// arrayArg is an arg that can be specified multiple times. It implements
// https://golang.org/pkg/flag/#Value an is based on
// https://stackoverflow.com/questions/28322997/how-to-get-a-list-of-values-into-a-flag-in-golang.
type arrayArg []string

func (a arrayArg) String() string {
	return strings.Join(a, ", ")
}

func (a *arrayArg) Set(val string) error {
	*a = append(*a, val)
	return nil
}
