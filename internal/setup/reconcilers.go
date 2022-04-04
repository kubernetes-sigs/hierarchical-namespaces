package setup

import (
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/hierarchical-namespaces/internal/anchor"
	"sigs.k8s.io/hierarchical-namespaces/internal/crd"
	"sigs.k8s.io/hierarchical-namespaces/internal/forest"
	"sigs.k8s.io/hierarchical-namespaces/internal/hierarchyconfig"
	"sigs.k8s.io/hierarchical-namespaces/internal/hncconfig"
)

type Options struct {
	MaxReconciles int
	ReadOnly      bool
	UseFakeClient bool
	NoWebhooks    bool
	HNCCfgRefresh time.Duration
}

func Create(log logr.Logger, mgr ctrl.Manager, f *forest.Forest, opts Options) {
	log.Info("Creating controllers", "opts", opts)

	if !opts.NoWebhooks {
		log.Info("Registering validating webhook (won't work when running locally; use --no-webhooks)")
		createWebhooks(mgr, f, opts)
	}

	log.Info("Registering reconcilers")
	if err := CreateReconcilers(mgr, f, opts); err != nil {
		log.Error(err, "cannot create controllers")
		os.Exit(1)
	}
}

// CreateReconcilers creates all reconcilers.
//
// This function is called both from main.go as well as from the integ tests.
func CreateReconcilers(mgr ctrl.Manager, f *forest.Forest, opts Options) error {
	if err := crd.Setup(mgr, opts.UseFakeClient); err != nil {
		return err
	}

	// Create Anchor reconciler.
	ar := &anchor.Reconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("anchor").WithName("reconcile"),
		Forest:   f,
		ReadOnly: opts.ReadOnly,
	}
	f.AddListener(ar)

	// Create the HNC Config reconciler.
	hnccfgr := &hncconfig.Reconciler{
		Client:          mgr.GetClient(),
		Log:             ctrl.Log.WithName("hncconfig").WithName("reconcile"),
		Manager:         mgr,
		Forest:          f,
		ReadOnly:        opts.ReadOnly,
		RefreshDuration: opts.HNCCfgRefresh,
	}

	// Create the HC reconciler with a pointer to the Anchor reconciler.
	hcr := &hierarchyconfig.Reconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("hierarchyconfig").WithName("reconcile"),
		Forest:   f,
		ReadOnly: opts.ReadOnly,
	}

	if err := ar.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("cannot create anchor reconciler: %s", err.Error())
	}
	if err := hnccfgr.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("cannot create Config reconciler: %s", err.Error())
	}
	if err := hcr.SetupWithManager(mgr, opts.MaxReconciles); err != nil {
		return fmt.Errorf("cannot create Hierarchy reconciler: %s", err.Error())
	}

	return nil
}
