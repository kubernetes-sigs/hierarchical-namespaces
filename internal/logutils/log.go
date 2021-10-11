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

package logutils

import (
	"sync/atomic"

	"github.com/go-logr/logr"
)

// reconcileID is used purely to set the "rid" field in the log, so we can tell which log messages
// were part of the same reconciliation attempt, even if multiple are running parallel (or it's
// simply hard to tell when one ends and another begins).
//
// The ID is shared among all reconcilers so that each log message gets a unique ID across all of
// HNC.
var nextReconcileID int64

// WithRID  adds a reconcile ID (rid) to the given logger.
func WithRID(log logr.Logger) logr.Logger {
	rid := atomic.AddInt64(&nextReconcileID, 1)
	return log.WithValues("rid", rid)
}
