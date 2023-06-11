// Package CRD has utilities relating to CRDs.
package crd

import (
	"context"
	"fmt"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

// fakeDeleteCRDClient is a fake client used for testing only
type fakeDeleteCRDClient struct{}

// fakeDeleteCRDClient doesn't return any err on Get() because none of the reconciler test performs CRD deletion
func (f fakeDeleteCRDClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return nil
}

// crdClientType could be either a real client or fakeDeleteCRDClient
type crdClientType interface {
	Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
}

// crdClient is an uncached client for checking CRD deletion (or a fake for testing)
var crdClient crdClientType

// Setup decides whether to use the fake client
func Setup(mgr ctrl.Manager, fake bool) error {
	if fake {
		crdClient = fakeDeleteCRDClient{}
		return nil
	}

	//TODO(aludwin): can we just use mgr.GetClient()? If not, document why not
	var err error
	crdClient, err = client.New(config.GetConfigOrDie(), client.Options{
		Scheme: mgr.GetScheme(),
		// I'm not sure if this mapper is needed - @ginnyji Dec2020
		Mapper: mgr.GetRESTMapper(),
	})
	if err != nil {
		return fmt.Errorf("cannot create crdClient: %s", err.Error())
	}

	return nil
}

// IsDeletingCRD returns true if the specified HNC CRD is being or has been deleted. The argument
// expected is the CRD name minus the HNC suffix, e.g. "hierarchyconfigurations".
func IsDeletingCRD(ctx context.Context, nm string) (bool, error) {
	crd := &apiextensions.CustomResourceDefinition{}
	nsn := types.NamespacedName{Name: nm + "." + api.MetaGroup}
	if err := crdClient.Get(ctx, nsn, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return !crd.DeletionTimestamp.IsZero(), nil
}
