package setup

import (
	"fmt"

	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/hierarchical-namespaces/internal/anchor"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/hierarchyconfig"
	"sigs.k8s.io/hierarchical-namespaces/internal/hncconfig"
	ns "sigs.k8s.io/hierarchical-namespaces/internal/namespace" // for some reason, by default this gets imported as "namespace*s*"
	"sigs.k8s.io/hierarchical-namespaces/internal/objects"
)

const (
	serviceName     = "hnc-webhook-service"
	vwhName         = "hnc-validating-webhook-configuration"
	mwhName         = "hnc-mutating-webhook-configuration"
	caName          = "hnc-ca"
	caOrganization  = "hnc"
	secretNamespace = "hnc-system"
	secretName      = "hnc-webhook-server-cert"
	certDir         = "/tmp/k8s-webhook-server/serving-certs"
)

// DNSName is <service name>.<namespace>.svc
var dnsName = fmt.Sprintf("%s.%s.svc", serviceName, secretNamespace)

// CreateCertsIfNeeded creates all certs for webhooks. This function is called from main.go.
func CreateCertsIfNeeded(mgr ctrl.Manager, novalidation, internalCert, restartOnSecretRefresh bool) (chan struct{}, error) {
	setupFinished := make(chan struct{})
	if novalidation || !internalCert {
		close(setupFinished)
		return setupFinished, nil
	}

	return setupFinished, cert.AddRotator(mgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		CertDir:        certDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        setupFinished,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.Validating,
			Name: vwhName,
		}, {
			Type: cert.Mutating,
			Name: mwhName,
		}},
		RestartOnSecretRefresh: restartOnSecretRefresh,
	})
}

// CreateWebhooks creates all mutators and validators. This function is called from main.go.
func CreateWebhooks(mgr ctrl.Manager, f *forest.Forest) {
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
}
