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

package kubectl

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/liggitt/tabwriter"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/printers"
	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

const (
	tabwriterMinWidth = 6
	tabwriterWidth    = 4
	tabwriterPadding  = 3
	tabwriterPadChar  = ' '
	tabwriterFlags    = tabwriter.RememberWidths
)

var namespace string

// Define HierarchicalResourceQuota Table Column
var hierarchicalResourceQuotaColumnDefinitions = []metav1.TableColumnDefinition{
	{Name: "Name", Type: "string", Format: "name", Description: metav1.ObjectMeta{}.SwaggerDoc()["name"]},
	{Name: "Age", Type: "string", Description: metav1.ObjectMeta{}.SwaggerDoc()["creationTimestamp"]},
	{Name: "Request", Type: "string", Description: "Request represents a minimum amount of cpu/memory that a container may consume."},
	{Name: "Limit", Type: "string", Description: "Limits control the maximum amount of cpu/memory that a container may use independent of contention on the node."},
}

var hrqCmd = &cobra.Command{
	Use:   "hrq [NAME]",
	Short: "Display one or more HierarchicalResourceQuota",
	Run:   Run,
}

func Run(cmd *cobra.Command, args []string) {
	flags := cmd.Flags()
	table := &metav1.Table{ColumnDefinitions: hierarchicalResourceQuotaColumnDefinitions}

	showLabels := flags.Changed("show-labels")

	allResourcesNamespaced := !flags.Changed("all-namespaces")
	if !allResourcesNamespaced {
		namespace = ""
	}

	// Get HierarchicalResourceQuotaList from the specified namespace
	hrqList := client.getHRQ(args, namespace)

	if len(hrqList.Items) < 1 {
		if allResourcesNamespaced {
			fmt.Printf("No resources found in %s namespace.\n", namespace)
			os.Exit(1)
		} else {
			fmt.Println("No resources found")
			os.Exit(1)
		}
	}

	// Create []metav1.TableRow from HierarchicalResourceQuotaList
	tableRaws, err := printHierarchicalResourceQuotaList(hrqList)
	if err != nil {
		fmt.Printf("Error reading hierarchicalresourcequotas: %s\n", err)
	}
	table.Rows = tableRaws

	// Create writer
	w := tabwriter.NewWriter(os.Stdout, tabwriterMinWidth, tabwriterWidth, tabwriterPadding, tabwriterPadChar, tabwriterFlags)

	// Create TablePrinter
	p := printers.NewTablePrinter(printers.PrintOptions{
		NoHeaders:        false,
		WithNamespace:    !allResourcesNamespaced,
		WithKind:         true,
		Wide:             true,
		ShowLabels:       showLabels,
		Kind:             schema.GroupKind{},
		ColumnLabels:     nil,
		SortBy:           "",
		AllowMissingKeys: false,
	})

	p.PrintObj(table, w)

	w.Flush()
}

func printHierarchicalResourceQuota(hierarchicalResourceQuota *api.HierarchicalResourceQuota) ([]metav1.TableRow, error) {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: hierarchicalResourceQuota},
	}

	resources := make([]v1.ResourceName, 0, len(hierarchicalResourceQuota.Status.Hard))
	for resource := range hierarchicalResourceQuota.Status.Hard {
		resources = append(resources, resource)
	}
	sort.Sort(SortableResourceNames(resources))

	requestColumn := bytes.NewBuffer([]byte{})
	limitColumn := bytes.NewBuffer([]byte{})
	for i := range resources {
		w := requestColumn
		resource := resources[i]
		usedQuantity := hierarchicalResourceQuota.Status.Used[resource]
		hardQuantity := hierarchicalResourceQuota.Status.Hard[resource]

		// use limitColumn writer if a resource name prefixed with "limits" is found
		if pieces := strings.Split(resource.String(), "."); len(pieces) > 1 && pieces[0] == "limits" {
			w = limitColumn
		}

		fmt.Fprintf(w, "%s: %s/%s, ", resource, usedQuantity.String(), hardQuantity.String())
	}

	age := translateTimestampSince(hierarchicalResourceQuota.CreationTimestamp)
	row.Cells = append(row.Cells, hierarchicalResourceQuota.Name, age, strings.TrimSuffix(requestColumn.String(), ", "), strings.TrimSuffix(limitColumn.String(), ", "))
	return []metav1.TableRow{row}, nil
}

func printHierarchicalResourceQuotaList(list *api.HierarchicalResourceQuotaList) ([]metav1.TableRow, error) {
	rows := make([]metav1.TableRow, 0, len(list.Items))
	for i := range list.Items {
		r, err := printHierarchicalResourceQuota(&list.Items[i])
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	return rows, nil
}

// translateTimestampSince returns the elapsed time since timestamp in
// human-readable approximation.
func translateTimestampSince(timestamp metav1.Time) string {
	if timestamp.IsZero() {
		return "<unknown>"
	}

	return duration.HumanDuration(time.Since(timestamp.Time))
}

// SortableResourceNames - An array of sortable resource names
type SortableResourceNames []v1.ResourceName

func (list SortableResourceNames) Len() int {
	return len(list)
}

func (list SortableResourceNames) Swap(i, j int) {
	list[i], list[j] = list[j], list[i]
}

func (list SortableResourceNames) Less(i, j int) bool {
	return list[i] < list[j]
}

func newHrqCmd() *cobra.Command {
	hrqCmd.Flags().StringVarP(&namespace, "namespace", "n", v1.NamespaceDefault, "If present, the namespace scope for this CLI request")

	hrqCmd.Flags().BoolP("all-namespaces", "A", false, "Displays all HierarchicalResourceQuota on the cluster")
	hrqCmd.Flags().BoolP("show-labels", "", false, "Displays Labels of HierarchicalResourceQuota")
	return hrqCmd
}
