// This file is a modified copy of k8s.io/kubernetes/pkg/quota/v1/resources_test.go
package utils

import (
	"reflect"
	"sort"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestEquals(t *testing.T) {
	type inputs struct {
		a v1.ResourceList
		b v1.ResourceList
	}
	tests := []struct {
		name string
		in   inputs
		want bool
	}{
		{
			name: "isEqual",
			in: inputs{
				a: v1.ResourceList{},
				b: v1.ResourceList{},
			},
			want: true,
		},
		{
			name: "isEqualWithKeys",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
				b: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			want: true,
		},
		{
			name: "isNotEqualSameKeys",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("200m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
				b: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			want: false,
		},
		{
			name: "isNotEqualDiffKeys",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
				b: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
					v1.ResourcePods:   resource.MustParse("1"),
				},
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Equals(tc.in.a, tc.in.b); got != tc.want {
				t.Errorf("%s want: %v, got: %v, a=%v, b=%v", tc.name,
					tc.want, got, tc.in.a, tc.in.b)
			}
		})
	}
}

func TestAdd(t *testing.T) {
	type inputs struct {
		a v1.ResourceList
		b v1.ResourceList
	}
	tests := []struct {
		name string
		in   inputs
		want v1.ResourceList
	}{
		{
			name: "empty",
			in: inputs{
				a: v1.ResourceList{},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{},
		},
		{
			name: "one input empty",
			in: inputs{
				a: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
				b: v1.ResourceList{}},
			want: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
		},
		{
			name: "both inputs non-empty",
			in: inputs{
				a: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
				b: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")}},
			want: v1.ResourceList{v1.ResourceCPU: resource.MustParse("200m")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Add(tc.in.a, tc.in.b)
			if result := Equals(tc.want, got); !result {
				t.Errorf("%s want: %v, got: %v, a=%v, b=%v", tc.name,
					tc.want, got, tc.in.a, tc.in.b)
			}
		})
	}
}

func TestAddIfExists(t *testing.T) {
	type inputs struct {
		a v1.ResourceList
		b v1.ResourceList
	}
	tests := []struct {
		name string
		in   inputs
		want v1.ResourceList
	}{
		{
			name: "noKeys",
			in: inputs{
				a: v1.ResourceList{},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{},
		},
		{
			name: "toEmpty",
			in: inputs{
				a: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{},
		},
		{
			name: "matching",
			in: inputs{
				a: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
				b: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
			},
			want: v1.ResourceList{v1.ResourceCPU: resource.MustParse("200m")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AddIfExists(tc.in.a, tc.in.b)
			if result := Equals(tc.want, got); !result {
				t.Errorf("%s want: %v, got: %v", tc.name, tc.want, got)
			}
		})
	}
}

func TestSubtract(t *testing.T) {
	type inputs struct {
		a v1.ResourceList
		b v1.ResourceList
	}
	tests := []struct {
		name string
		in   inputs
		want v1.ResourceList
	}{
		{
			name: "noKeys",
			in: inputs{
				a: v1.ResourceList{},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{},
		},
		{
			name: "value-empty",
			in: inputs{
				a: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
		},
		{
			name: "empty-value",
			in: inputs{
				a: v1.ResourceList{},
				b: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
			},
			want: v1.ResourceList{v1.ResourceCPU: resource.MustParse("-100m")},
		},
		{
			name: "value-value",
			in: inputs{
				a: v1.ResourceList{v1.ResourceCPU: resource.MustParse("200m")},
				b: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
			},
			want: v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Subtract(tc.in.a, tc.in.b)
			if result := Equals(tc.want, got); !result {
				t.Errorf("%s want: %v, got: %v", tc.name, tc.want, got)
			}
		})
	}
}

func TestCopy(t *testing.T) {
	tests := []struct {
		name string
		in   v1.ResourceList
		want v1.ResourceList
	}{
		{
			name: "noKeys",
			in:   v1.ResourceList{},
			want: v1.ResourceList{},
		},
		{
			name: "hasKeys",
			in: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("100m"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
				v1.ResourcePods:   resource.MustParse("1")},
			want: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("100m"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
				v1.ResourcePods:   resource.MustParse("1")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Copy(tc.in)
			if result := Equals(tc.want, got); !result {
				t.Errorf("%s want: %v, got: %v", tc.name, tc.want, got)
			}
		})
	}
}

func TestMin(t *testing.T) {
	type inputs struct {
		a v1.ResourceList
		b v1.ResourceList
	}
	tests := []struct {
		name string
		in   inputs
		want v1.ResourceList
	}{
		{
			name: "two empty lists",
			in: inputs{
				a: v1.ResourceList{},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{},
		},
		{
			name: "one empty list",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:     resource.MustParse("100m"),
					v1.ResourceSecrets: resource.MustParse("10")},
				b: v1.ResourceList{},
			},
			want: v1.ResourceList{
				v1.ResourceCPU:     resource.MustParse("100m"),
				v1.ResourceSecrets: resource.MustParse("10")},
		},
		{
			name: "two lists have no common resource types",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:     resource.MustParse("100m"),
					v1.ResourceSecrets: resource.MustParse("10")},
				b: v1.ResourceList{v1.ResourcePods: resource.MustParse("6")},
			},
			want: v1.ResourceList{
				v1.ResourceCPU:     resource.MustParse("100m"),
				v1.ResourceSecrets: resource.MustParse("10"),
				v1.ResourcePods:    resource.MustParse("6")},
		},
		{
			name: "two lists have common resource types",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:     resource.MustParse("25m"),
					v1.ResourceSecrets: resource.MustParse("20")},
				b: v1.ResourceList{
					v1.ResourcePods:    resource.MustParse("6"),
					v1.ResourceCPU:     resource.MustParse("100m"),
					v1.ResourceSecrets: resource.MustParse("10")},
			},
			want: v1.ResourceList{
				v1.ResourcePods:    resource.MustParse("6"),
				v1.ResourceCPU:     resource.MustParse("25m"),
				v1.ResourceSecrets: resource.MustParse("10")},
		},
		{
			name: "some resource types have same quantities",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:     resource.MustParse("25m"),
					v1.ResourceSecrets: resource.MustParse("20")},
				b: v1.ResourceList{
					v1.ResourcePods:    resource.MustParse("6"),
					v1.ResourceCPU:     resource.MustParse("25m"),
					v1.ResourceSecrets: resource.MustParse("10")},
			},
			want: v1.ResourceList{
				v1.ResourcePods:    resource.MustParse("6"),
				v1.ResourceCPU:     resource.MustParse("25m"),
				v1.ResourceSecrets: resource.MustParse("10")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Min(tc.in.a, tc.in.b)
			if result := Equals(tc.want, got); !result {
				t.Errorf("%s want: %v, got: %v", tc.name, tc.want, got)
			}
		})
	}
}

func TestResourceNames(t *testing.T) {
	tests := []struct {
		name string
		in   v1.ResourceList
		want []v1.ResourceName
	}{
		{
			name: "empty",
			in:   v1.ResourceList{},
			want: []v1.ResourceName{},
		},
		{
			name: "non-empty",
			in: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("100m"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
			},
			want: []v1.ResourceName{v1.ResourceMemory, v1.ResourceCPU},
		},
	}
	for _, tc := range tests {
		got := ResourceNames(tc.in)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sort.Slice(tc.want, func(i, j int) bool { return tc.want[i] < tc.want[j] })

		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s expected: %v, actual: %v", tc.name, tc.want, got)
		}
	}
}

func TestContains(t *testing.T) {
	type inputs struct {
		a []v1.ResourceName
		b v1.ResourceName
	}
	tests := []struct {
		name string
		in   inputs
		want bool
	}{
		{
			name: "empty slice",
			in: inputs{
				a: []v1.ResourceName{},
				b: v1.ResourceCPU,
			},
			want: false,
		},
		{
			name: "does not contain",
			in: inputs{
				a: []v1.ResourceName{v1.ResourceMemory},
				b: v1.ResourceCPU,
			},
			want: false,
		},
		{
			name: "contains",
			in: inputs{
				a: []v1.ResourceName{v1.ResourceMemory, v1.ResourceCPU},
				b: v1.ResourceCPU,
			},
			want: true,
		},
	}
	for _, tc := range tests {
		if got := Contains(tc.in.a, tc.in.b); got != tc.want {
			t.Errorf("%s expected: %v, actual: %v", tc.name, tc.want, got)
		}
	}
}

func TestMask(t *testing.T) {
	type inputs struct {
		a v1.ResourceList
		b []v1.ResourceName
	}
	tests := []struct {
		name string
		in   inputs
		want v1.ResourceList
	}{
		{
			name: "empty list empty names",
		},
		{
			name: "empty list",
			in: inputs{
				b: []v1.ResourceName{
					v1.ResourceSecrets,
				},
			},
		},
		{
			name: "empty name",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
		{
			name: "list does not contain resources of given names",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
				b: []v1.ResourceName{
					v1.ResourceSecrets,
				},
			},
		},
		{
			name: "list contains resources of given names",
			in: inputs{
				a: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
				b: []v1.ResourceName{
					v1.ResourceCPU,
				},
			},
			want: v1.ResourceList{
				v1.ResourceCPU: resource.MustParse("100m")},
		},
	}
	for _, tc := range tests {
		if got := Mask(tc.in.a, tc.in.b); !Equals(tc.want, got) {
			t.Errorf("%s expected: %v, actual: %v", tc.name, tc.want, got)
		}
	}
}

func TestToSet(t *testing.T) {
	tests := []struct {
		name string
		in   []v1.ResourceName
		want sets.String
	}{
		{
			name: "empty resource names",
		},
		{
			name: "no duplicate resource name",
			in: []v1.ResourceName{
				v1.ResourceCPU,
				v1.ResourceMemory,
			},
			want: sets.NewString(
				v1.ResourceCPU.String(),
				v1.ResourceMemory.String()),
		},
		{
			name: "has duplicate resource name",
			in: []v1.ResourceName{
				v1.ResourceCPU,
				v1.ResourceCPU,
				v1.ResourceMemory,
			},
			want: sets.NewString(
				v1.ResourceCPU.String(),
				v1.ResourceMemory.String()),
		},
	}
	for _, tc := range tests {
		got := toSet(tc.in)
		if !got.Equal(tc.want) {
			t.Errorf("%s expected: %v, actual: %v", tc.name, tc.want, got)
		}
	}
}

func TestCleanupUnneeded(t *testing.T) {
	tests := []struct {
		name   string
		usages v1.ResourceList
		limits v1.ResourceList
		want   v1.ResourceList
	}{
		{
			name: "usages with empty limits; should get empty list",
			usages: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("0"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
				v1.ResourcePods:   resource.MustParse("1"),
			},
		},
		{
			name: "one usage matching the limits; should list the matching usage",
			usages: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("100m"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
				v1.ResourcePods:   resource.MustParse("1"),
			},
			limits: v1.ResourceList{
				// Add a random resource from the usage list to the limits.
				v1.ResourceCPU: resource.MustParse("100m"),
			},
			want: v1.ResourceList{
				v1.ResourceCPU: resource.MustParse("100m"),
			},
		},
		{
			name: "matching usage is zero and no other matching usages; should list the matching usage even if it's empty",
			usages: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("0"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
				v1.ResourcePods:   resource.MustParse("0"),
			},
			limits: v1.ResourceList{
				// Add one of the zero-quantity resource in limits.
				v1.ResourcePods: resource.MustParse("1"),
			},
			want: v1.ResourceList{
				v1.ResourcePods: resource.MustParse("0"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanupUnneeded(tc.usages, tc.limits)
			if result := Equals(tc.want, got); !result {
				t.Errorf("%s want: %v, got: %v", tc.name, tc.want, got)
			}
		})
	}
}
