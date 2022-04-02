package hrq

import (
	"context"
	"errors"
	"testing"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

func TestHRQSpec(t *testing.T) {
	l := zap.New()
	tests := []struct {
		name     string
		hrqLimit []string
		fail     bool
		msg      string
	}{
		{
			name:     "allow one standard resource",
			hrqLimit: []string{"configmaps", "1"},
		}, {
			name:     "allow multiple standard resources",
			hrqLimit: []string{"configmaps", "1", "secrets", "1"},
		}, {
			name:     "deny non-standard resource",
			hrqLimit: []string{"foobar", "1"},
			fail:     true,
			msg:      "'foobar' is not a valid quota type",
		}, {
			name:     "deny a mix of standard and non-standard resources",
			hrqLimit: []string{"foobar", "1", "configmaps", "1"},
			fail:     true,
			msg:      "'foobar' is not a valid quota type",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hrq := &api.HierarchicalResourceQuota{}
			hrq.Spec.Hard = argsToResourceList(tc.hrqLimit...)
			v := HRQ{server: fakeServer("")}
			got := v.handle(context.Background(), l, hrq)
			if got.AdmissionResponse.Allowed == tc.fail || got.Result.Message != tc.msg {
				t.Errorf("unexpected admission response. Expected: %t, %s; Got: %t, %s", !tc.fail, tc.msg, got.AdmissionResponse.Allowed, got.Result.Message)
			}
		})
	}
}

// fakeServer implements serverClient.
type fakeServer string

// validate returns an error if the RQ has invalid quota types, "foobar"
// specifically in this test. Otherwise, return no error.
func (f fakeServer) validate(ctx context.Context, inst *v1.ResourceQuota) error {
	if _, ok := inst.Spec.Hard["foobar"]; ok {
		return errors.New("'foobar' is not a valid quota type")
	}
	return nil
}
