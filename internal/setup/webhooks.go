package setup

import (
	"fmt"

	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/hierarchical-namespaces/internal/anchor"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/hierarchyconfig"
	"sigs.k8s.io/hierarchical-namespaces/internal/hncconfig"
	"sigs.k8s.io/hierarchical-namespaces/internal/hrq"
	ns "sigs.k8s.io/hierarchical-namespaces/internal/namespace" // for some reason, by default this gets imported as "namespace*s*"
	"sigs.k8s.io/hierarchical-namespaces/internal/objects"
	"sigs.k8s.io/hierarchical-namespaces/internal/webhooks"
)

const (
	serviceName       = "hnc-webhook-service"
	caName         = "hnc-ca"
	caOrganization = "hnc"
	secretName     = "hnc-webhook-server-cert"
	certDir        = "/tmp/k8s-webhook-server/serving-certs"apiExtCertDir     = "/certs"
	apiExtServiceName = "hnc-resourcelist"
	apiExtSecretName  = "hnc-resourcelist"
	apiExtName        = "v1alpha2.resources.hnc.x-k8s.io"
)

// ManageCerts creates all certs for webhooks and apiservices. This function is called from main.go.
func ManageCerts(mgr ctrl.Manager, setupFinished chan struct{}, restartOnSecretRefresh bool) error {
	hncNamespace := config.GetHNCNamespace()
	// DNSName is <service name>.<hncNamespace>.svc
	dnsName := fmt.Sprintf("%s.%s.svc", serviceName, hncNamespace)

	err := cert.AddRotator(mgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: hncNamespace,
			Name:      secretName,
		},
		CertDir:        certDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        setupFinished,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.Validating,
			Name: webhooks.ValidatingWebhookConfigurationName,
		}, {
			Type: cert.Mutating,
			Name: webhooks.MutatingWebhookConfigurationName,
		}},
		RestartOnSecretRefresh: restartOnSecretRefresh,
	})
	if err != nil {
		return err
	}
	apiExtDNSName := fmt.Sprintf("%s.%s.svc", apiExtServiceName, hncNamespace)
	return cert.AddRotator(mgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: hncNamespace,
			Name:      apiExtSecretName,
		},
		CertDir:        apiExtCertDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        apiExtDNSName,
		IsReady:        setupFinished,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.APIService,
			Name: apiExtName,
		}},
		RestartOnSecretRefresh: restartOnSecretRefresh,
	})
}

// createWebhooks creates all mutators and validators.
func createWebhooks(mgr ctrl.Manager, f *forest.Forest, opts Options) {
	// Create webhook for Hierarchy
	mgr.GetWebhookServer().Register(hierarchyconfig.ServingPath, &webhook.Admission{Handler: &hierarchyconfig.Validator{
		Log:    ctrl.Log.WithName("hierarchyconfig").WithName("validate"),
		Forest: f,
	}})

	// Create webhooks for managed objects
	mgr.GetWebhookServer().Register(objects.ServingPath, &webhook.Admission{Handler: &objects.Validator{
		Log:    ctrl.Log.WithName("objects").WithName("validate"),
		Forest: f,
	}})

	// Create webhook for the config
	mgr.GetWebhookServer().Register(hncconfig.ServingPath, &webhook.Admission{Handler: &hncconfig.Validator{
		Log:    ctrl.Log.WithName("hncconfig").WithName("validate"),
		Forest: f,
	}})

	// Create webhook for the subnamespace anchors.
	mgr.GetWebhookServer().Register(anchor.ServingPath, &webhook.Admission{Handler: &anchor.Validator{
		Log:    ctrl.Log.WithName("anchor").WithName("validate"),
		Forest: f,
	}})

	// Create webhook for the namespaces (core type).
	mgr.GetWebhookServer().Register(ns.ServingPath, &webhook.Admission{Handler: &ns.Validator{
		Log:    ctrl.Log.WithName("namespace").WithName("validate"),
		Forest: f,
	}})

	// Create mutator for namespace `included-namespace` label.
	mgr.GetWebhookServer().Register(ns.MutatorServingPath, &webhook.Admission{Handler: &ns.Mutator{
		Log: ctrl.Log.WithName("namespace").WithName("mutate"),
	}})

	if opts.HRQ {
		// Create webhook for ResourceQuota status.
		mgr.GetWebhookServer().Register(hrq.ResourceQuotasStatusServingPath, &webhook.Admission{Handler: &hrq.ResourceQuotaStatus{
			Log:    ctrl.Log.WithName("validators").WithName("ResourceQuota"),
			Forest: f,
		}})

		// Create webhook for HierarchicalResourceQuota spec.
		mgr.GetWebhookServer().Register(hrq.HRQServingPath, &webhook.Admission{Handler: &hrq.HRQ{
			Log: ctrl.Log.WithName("validators").WithName("HierarchicalResourceQuota"),
		}})
	}
}
