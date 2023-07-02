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
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete -n PARENT CHILD",
	Short: "Deletes a subnamespace under the given parent.",
	Example: `# Delete the 'foo' anchor in the parent 'bar' namespace
	kubectl hns delete foo -n bar

	# Allow 'foo' to be cascading deleted and delete it
	kubectl hns delete foo -n bar --allowCascadingDeletion`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		nnm := args[0]

		parent, _ := cmd.Flags().GetString("namespace")
		if parent == "" {
			fmt.Println("Error: parent must be set via --namespace or -n")
			os.Exit(1)
		}

		allowCD, _ := cmd.Flags().GetBool("allowCascadingDeletion")
		if allowCD {
			hc := client.getHierarchy(nnm)
			if hc.Spec.AllowCascadingDeletion {
				fmt.Printf("Cascading deletion for '%s' is already set to 'true'; unchanged\n", nnm)
			} else {
				fmt.Printf("Allowing cascading deletion on '%s'\n", nnm)
				hc.Spec.AllowCascadingDeletion = true
				client.updateHierarchy(hc, fmt.Sprintf("update the hierarchical configuration of %s", nnm))
			}
		}

		client.deleteAnchor(parent, nnm)
	},
}

func newDeleteCmd() *cobra.Command {
	deleteCmd.Flags().StringP("namespace", "n", "", "The parent namespace for the new subnamespace")
	deleteCmd.Flags().BoolP("allowCascadingDeletion", "a", false, "Allows cascading deletion of its subnamespaces.")
	return deleteCmd
}
