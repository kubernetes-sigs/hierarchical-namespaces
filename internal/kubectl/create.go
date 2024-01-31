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

var createCmd = &cobra.Command{
	Use:   "create -n PARENT CHILD [-a] key1=value1 [-l] key1=value1,key2=value2",
	Short: "Creates a subnamespace under the given parent.",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		parent, _ := cmd.Flags().GetString("namespace")
		if parent == "" {
			fmt.Println("Error: parent must be set via --namespace or -n")
			os.Exit(1)
		}
		annotations, _ := cmd.Flags().GetStringToString("annotations")
		labels, _ := cmd.Flags().GetStringToString("labels")

		// Create the anchor, the custom resource representing the subnamespace.
		client.createAnchor(parent, args[0], annotations, labels)
	},
}

func newCreateCmd() *cobra.Command {
	createCmd.Flags().StringP("namespace", "n", "", "The parent namespace for the new subnamespace")
	createCmd.Flags().StringToStringP("annotations", "a", make(map[string]string, 0), "The annotations to add on subnamespace")
	createCmd.Flags().StringToStringP("labels", "l", make(map[string]string, 0), "The labels to add on subnamespace")
	return createCmd
}
