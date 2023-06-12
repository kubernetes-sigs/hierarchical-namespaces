package kubectl

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

var (
	watch         bool
	parent        string
	allNamespaces bool
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "list resources",
	RunE: func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("not enough arguments to 'get'")
		}
		if len(args) > 1 {
			return fmt.Errorf("too many arguments to 'get'")
		}
		d := newDoer(args[0])
		rv, err := d.get(args[0])
		if err != nil {
			return err
		}
		if watch {
			return d.watch(args[0], rv)
		}
		return nil
	},
}

func newGetCmd() *cobra.Command {
	return getCmd
}

func init() {
	getCmd.Flags().BoolVarP(&watch, "watch", "w", false, "watch for changes")
	getCmd.PersistentFlags().StringVarP(&parent, "namespace", "n", "default", "parent namespace to scope request to")
	getCmd.PersistentFlags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "list across all namespaces (equivalent to kubectl get <resource> -A)")
}

type doer struct {
	client  dynamic.ResourceInterface
	printer printers.ResourcePrinter
}

func newDoer(resource string) doer {
	client := getClient(resource)
	// See https://iximiuz.com/en/posts/kubernetes-api-go-cli/#pretty-print-kubernetes-object-as-yamljsontables for information on k8s printers
	printOpts := printers.PrintOptions{WithNamespace: true}
	return doer{
		client:  client,
		printer: printers.NewTypeSetter(scheme.Scheme).ToPrinter(printers.NewTablePrinter(printOpts)),
	}
}

func getClient(resource string) dynamic.ResourceInterface {
	gvr := api.ResourcesGroupVersion.WithResource(resource)
	gvr, _ = mapper.ResourceFor(gvr)
	resourceClient := dynamicClient.Resource(gvr)
	if allNamespaces {
		return resourceClient
	}
	return resourceClient.Namespace(parent)
}

func (d doer) get(resource string) (string, error) {
	unst, err := d.client.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	rv := unst.GetResourceVersion()
	if err = d.printer.PrintObj(unst, os.Stdout); err != nil {
		return rv, err
	}
	return rv, nil
}

func (d doer) watch(resource, rv string) error {
	if rv == "" {
		rv = "0"
	}
	opts := metav1.ListOptions{
		ResourceVersion: rv,
	}
	watcher, err := d.client.Watch(context.TODO(), opts)
	if err != nil {
		return err
	}
	for event := range watcher.ResultChan() {
		if err = d.printer.PrintObj(event.Object, os.Stdout); err != nil {
			return err
		}
	}
	return nil
}
