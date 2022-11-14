package forest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/hierarchical-namespaces/internal/hrq/utils"
)

// testNS represents a namespace and how it looks in the forest, with ancestors,
// one aggregate hrq, etc.
type testNS struct {
	nm string
	// There may be multiple ancestors. The names are divided by a space.
	ancs string
	// There is at most one hrq in each namespace in this test. Omitting the field
	// means there's no hrq in the namespace.
	limits string
	local  string
}

// testReq represents a request with namespace name and resource usages. Note:
// the usages are absolute usage numbers.
type testReq struct {
	ns  string
	use string
}

// usages maps a namespace name to the resource usages in string,
// e.g. "foo": "secrets 1", "bar": "secrets 2 pods 1".
type usages map[string]string

// hrqs maps an hrq name to the resource limits in string,
// e.g. "H1": "secrets 1", "H2": "secrets 2 pods 1".
type hrqs map[string]string

// wantError represents an error result.
type wantError struct {
	// The namespace that contains the HierarchicalResourceQuota
	// object whose limits are exceeded.
	ns string
	// The name of a HierarchicalResourceQuota object.
	nm string
	// Resources that exceed HRQ limits, with requested, used, limited quota.
	exceeded []exceeded
}

// exceeded represents one exceeded resource with requested, used, limited quota.
// Please note "exceeded" here means exceeding HRQ, not regular RQ. The quota
// string must be  in the form that can be parsed by resource.MustParse, e.g.
// "5", "100m", etc.
type exceeded struct {
	name string
	// requested is the additional quantity to use in the namespace, which resourceEquals
	// to (proposed usage - local usage).
	requested string
	// used is the quantity used for HRQ, not a local usage.
	used string
	// limited is the quota limited by HRQ.
	limited string
}

func TestTryUseResources(t *testing.T) {
	tests := []struct {
		name     string
		setup    []testNS
		req      testReq
		expected usages
		error    []wantError
	}{{
		name:     "allow nil change when under limit in single namespace",
		setup:    []testNS{{nm: "foo", limits: "secrets 3", local: "secrets 2"}},
		req:      testReq{ns: "foo", use: "secrets 2"},
		expected: usages{"foo": "secrets 2"},
	}, {
		name: "allow increase in child when under limit in both parent and child",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
		},
		req:      testReq{ns: "bar", use: "secrets 2"},
		expected: usages{"foo": "secrets 3", "bar": "secrets 2"},
	}, {
		name: "allow increase in child when under limit in both parent and child, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 1"},
		},
		req:      testReq{ns: "bar", use: "secrets 2 pods 1"},
		expected: usages{"foo": "secrets 3", "bar": "secrets 2 pods 1"},
	}, {
		name: "allow underused resources to increase even though other resources are over their limits",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 4"},
		},
		req:      testReq{ns: "bar", use: "secrets 2 pods 4"},
		expected: usages{"foo": "secrets 3", "bar": "secrets 2 pods 4"},
	}, {
		name: "deny increase when it would exceed limit in parent but not child",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
		},
		req:   testReq{ns: "bar", use: "secrets 2"},
		error: []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "1", used: "2", limited: "2"}}}},
		// Usages should not be updated.
		expected: usages{"foo": "secrets 2", "bar": "secrets 1"},
	}, {
		name: "deny increase in sibling when it would exceed limit in parent but not in children",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
			{nm: "baz", ancs: "foo"},
		},
		req:   testReq{ns: "baz", use: "secrets 1"},
		error: []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "1", used: "2", limited: "2"}}}},
		// Usages should not be updated. Note "bar" is not here because the expected
		// usages here are for the request namespace and its ancestors.
		expected: usages{"foo": "secrets 2"},
	}, {
		name: "deny increase when it would exceed limit in parent but not child, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 1"},
		},
		req:   testReq{ns: "bar", use: "secrets 2 pods 1"},
		error: []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "1", used: "2", limited: "2"}}}},
		// Usages should not be updated.
		expected: usages{"foo": "secrets 2", "bar": "secrets 1 pods 1"},
	}, {
		name: "allow decrease under limit in both parent and child",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 2"},
		},
		req:      testReq{ns: "bar", use: "secrets 1"},
		expected: usages{"foo": "secrets 2", "bar": "secrets 1"},
	}, {
		name: "allow decrease under limit in both parent and child, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 2 pods 1"},
		},
		req:      testReq{ns: "bar", use: "secrets 1 pods 1"},
		expected: usages{"foo": "secrets 2", "bar": "secrets 1 pods 1"},
	}, {
		name: "allow decrease if it still exceeds limits",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 4"},
		},
		req:      testReq{ns: "bar", use: "secrets 2"},
		expected: usages{"foo": "secrets 3", "bar": "secrets 2"},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forest := buildForest(t, tc.setup)
			n := forest.Get(tc.req.ns)
			gotError := n.TryUseResources(stringToResourceList(t, tc.req.use))
			errMsg := checkError(gotError, tc.error)
			if errMsg != "" {
				t.Error(errMsg)
			}
			msg := checkUsages(t, n, tc.expected)
			if msg != "" {
				t.Error(msg)
			}
		})
	}
}

func TestCanUseResource(t *testing.T) {
	tests := []struct {
		name  string
		setup []testNS
		req   testReq
		want  []wantError
	}{{
		// `req.use` (i.e., ResourceQuota.Status.Used updates) is nil in this case
		// because when there is no parent and no limit, the corresponding
		// ResourceQuota object should not have any hard limit in ResourceQuota.Hard;
		// therefore, there is no ResourceQuota.Status.Used updates.
		name:  "allow nil change when no parent no limit",
		setup: []testNS{{nm: "foo"}},
		req:   testReq{ns: "foo"},
	}, {
		name:  "allow change when no parent no limit",
		setup: []testNS{{nm: "foo"}},
		req:   testReq{ns: "foo", use: "secrets 2"},
	}, {
		name:  "allow change under limit when no parent",
		setup: []testNS{{nm: "foo", limits: "secrets 3", local: "secrets 1"}},
		req:   testReq{ns: "foo", use: "secrets 1"},
	}, {
		name:  "deny increase exceeding local limit when no parent",
		setup: []testNS{{nm: "foo", limits: "secrets 3", local: "secrets 1"}},
		req:   testReq{ns: "foo", use: "secrets 4"},
		want:  []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "3", used: "1", limited: "3"}}}},
	}, {
		name: "deny increase exceeding parent limit",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
		},
		req:  testReq{ns: "bar", use: "secrets 3"},
		want: []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "2", used: "2", limited: "2"}}}},
	}, {
		name: "deny increase in sibling exceeding parent limit",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
			{nm: "baz", ancs: "foo"},
		},
		req:  testReq{ns: "baz", use: "secrets 1"},
		want: []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "1", used: "2", limited: "2"}}}},
	}, {
		name: "deny increase exceeding parent limit, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 1"},
		},
		req:  testReq{ns: "bar", use: "secrets 3 pods 1"},
		want: []wantError{{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "2", used: "2", limited: "2"}}}},
	}, {
		name: "allow decrease exceeding local limit when has parent",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "pods 2", local: "pods 4"},
		},
		req: testReq{ns: "bar", use: "pods 3"},
	}, {
		name: "allow decrease exceeding local limit when has parent, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 4"},
		},
		req: testReq{ns: "bar", use: "secrets 1 pods 3"},
	}, {
		name: "allow decrease exceeding parent limit when has parent",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 4"},
		},
		req: testReq{ns: "bar", use: "secrets 3"},
	}, {
		name: "allow decrease exceeding parent limit when has parent, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 4 pods 2"},
		},
		req: testReq{ns: "bar", use: "secrets 3 pods 2"},
	}, {
		name: "deny multiple resources exceeding limits",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 1"},
		},
		req: testReq{ns: "bar", use: "secrets 3 pods 4"},
		want: []wantError{
			{ns: "foo", nm: "fooHrq", exceeded: []exceeded{{name: "secrets", requested: "2", used: "2", limited: "2"}}},
			{ns: "bar", nm: "barHrq", exceeded: []exceeded{{name: "pods", requested: "3", used: "1", limited: "2"}}},
		},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := buildForest(t, tc.setup)
			got := f.Get(tc.req.ns).canUseResources(stringToResourceList(t, tc.req.use))
			errMsg := checkError(got, tc.want)
			if errMsg != "" {
				t.Error(errMsg)
			}
		})
	}
}

func TestUseResource(t *testing.T) {
	tests := []struct {
		name     string
		setup    []testNS
		req      testReq
		expected usages
	}{{
		// `req.use` (i.e., ResourceQuota.Status.Used updates) is nil in this case
		// because when there is no parent and no limit, the corresponding
		// ResourceQuota object should not have any hard limit in ResourceQuota.Hard;
		// therefore there is no ResourceQuota.Status.Used updates.
		name:  "nil change when no parent no limit",
		setup: []testNS{{nm: "foo"}},
		req:   testReq{ns: "foo"},
	}, {
		name:     "increase when no parent",
		setup:    []testNS{{nm: "foo", limits: "secrets 3", local: "secrets 1"}},
		req:      testReq{ns: "foo", use: "secrets 2"},
		expected: usages{"foo": "secrets 2"},
	}, {
		name: "increase in child when has parent",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
		},
		req:      testReq{ns: "bar", use: "secrets 2"},
		expected: usages{"foo": "secrets 3", "bar": "secrets 2"},
	}, {
		name: "increase in sibling when has parent",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 1"},
			{nm: "baz", ancs: "foo"},
		},
		req: testReq{ns: "baz", use: "secrets 1"},
		// Note: "bar" is not listed here because the expected usages are for
		// requested namespace and its ancestors.
		expected: usages{"foo": "secrets 3", "baz": "secrets 1"},
	}, {
		name: "increase in child when has parent, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 4"},
		},
		req:      testReq{ns: "bar", use: "secrets 2 pods 4"},
		expected: usages{"foo": "secrets 3", "bar": "secrets 2 pods 4"},
	}, {
		name: "decrease in child when has parent",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100", local: "secrets 2"},
		},
		req:      testReq{ns: "bar", use: "secrets 1"},
		expected: usages{"foo": "secrets 2", "bar": "secrets 1"},
	}, {
		name: "decrease in child when has parent, while not affecting other resource usages",
		setup: []testNS{
			{nm: "foo", limits: "secrets 3", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 2 pods 1"},
		},
		req:      testReq{ns: "bar", use: "secrets 1 pods 1"},
		expected: usages{"foo": "secrets 2", "bar": "secrets 1 pods 1"},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := buildForest(t, tc.setup)
			n := f.Get(tc.req.ns)
			n.UseResources(stringToResourceList(t, tc.req.use))
			msg := checkUsages(t, n, tc.expected)
			if msg != "" {
				t.Error(msg)
			}
		})
	}
}

func TestCheckLimits(t *testing.T) {
	type wantError struct {
		nm       string
		exceeded []corev1.ResourceName
	}
	tests := []struct {
		name      string
		limit     hrqs
		usage     string
		wantError wantError
	}{{
		name:  "no limits no usages",
		limit: hrqs{},
		usage: "",
	}, {
		name:  "no limits has usages",
		limit: hrqs{},
		usage: "secrets 1 pods 2",
	}, {
		name: "usage not in the limits",
		limit: hrqs{
			"H1": "cpu 100m",
		},
		usage: "secrets 1 pods 2",
	}, {
		name:  "usage not exceeds limits",
		limit: hrqs{"H1": "cpu 100m"},
		usage: "cpu 10m",
	}, {
		name:  "usages exceeds limits",
		limit: hrqs{"H1": "cpu 100m"},
		usage: "cpu 200m",
		wantError: wantError{
			nm:       "H1",
			exceeded: []corev1.ResourceName{corev1.ResourceCPU},
		},
	}, {
		name:  "multiple usages exceed limits",
		limit: hrqs{"H1": "cpu 100m secrets 10"},
		usage: "cpu 200m secrets 100",
		wantError: wantError{
			nm:       "H1",
			exceeded: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceSecrets},
		},
	}, {
		name: "multiple limits; usages exceed one limit",
		limit: hrqs{
			"H1": "cpu 100m secrets 10",
			"H2": "configmaps 3",
		},
		usage: "cpu 10m secrets 8 configmaps 5",
		wantError: wantError{
			nm:       "H2",
			exceeded: []corev1.ResourceName{corev1.ResourceConfigMaps},
		},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limits := limits{}
			for nm, l := range tc.limit {
				limits[nm] = stringToResourceList(t, l)
			}
			allowed, nm, exceeded := checkLimits(limits, stringToResourceList(t, tc.usage))
			expectAllowed := tc.wantError.nm == ""
			if allowed != expectAllowed ||
				nm != tc.wantError.nm ||
				!resourceEquals(exceeded, tc.wantError.exceeded) {
				t.Errorf("%s \n"+
					"wantError:\n"+
					"allowed: %t\n"+
					"nm: %s\n"+
					"exceeded: %v\n"+
					"got:\n"+
					"allowed: %t\n"+
					"nm: %s\n"+
					"exceeded: %v\n",
					tc.name,
					expectAllowed, tc.wantError.nm, tc.wantError.exceeded,
					allowed, nm, exceeded)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	tests := []struct {
		name  string
		setup []testNS
		ns    string
		want  string
	}{{
		name:  "no parent no limit",
		setup: []testNS{{nm: "foo"}},
		ns:    "foo",
		want:  "",
	}, {
		name:  "no parent",
		setup: []testNS{{nm: "foo", limits: "secrets 3", local: "secrets 1"}},
		ns:    "foo",
		want:  "secrets 3",
	}, {
		name: "has parent",
		setup: []testNS{
			{nm: "foo", limits: "secrets 2", local: "secrets 1"},
			{nm: "bar", ancs: "foo", limits: "secrets 100 pods 2", local: "secrets 1 pods 1"},
		},
		ns:   "bar",
		want: "secrets 2 pods 2",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := buildForest(t, tc.setup)
			n := f.Get(tc.ns)
			got := n.Limits()
			if result := utils.Equals(stringToResourceList(t, tc.want), got); !result {
				t.Errorf("%s wantError: %v, got: %v", tc.name, tc.want, got)
			}
		})
	}
}

// buildForest builds a forest from a list of testNS.
func buildForest(t *testing.T, nss []testNS) *Forest {
	t.Helper()
	f := &Forest{namespaces: namedNamespaces{}}
	// Create all namespaces in the forest, with their hrqs if there's one.
	for _, ns := range nss {
		if ns.nm == "" {
			t.Fatal("namespace has an empty name")
		}
		cur := f.Get(ns.nm)
		if ns.limits == "" {
			continue
		}
		cur.quotas = quotas{
			limits: limits{ns.nm + "Hrq": stringToResourceList(t, ns.limits)},
			used: usage{
				local:   stringToResourceList(t, ns.local),
				subtree: stringToResourceList(t, ns.local),
			},
		}
	}
	// Set ancestors from existing namespaces.
	for _, ns := range nss {
		cur := f.Get(ns.nm)
		for _, anc := range strings.Fields(ns.ancs) {
			next := f.Get(anc)
			cur.SetParent(next)
			cur = next
		}
	}
	return f
}

// checkError checks if an error is expected. It returns a message with details
// if the error is not ONE of the expected; otherwise, it returns an empty string.
func checkError(err error, wes []wantError) string {
	if err == nil {
		if len(wes) > 0 {
			return "expects error while actual run does " +
				"not return any error"
		}
	} else {
		if len(wes) > 0 {
			expected := ""
			matches := false
			for _, we := range wes {
				expected += fmt.Sprintf("%s, or ", we)
				if errorMatches(err, we) {
					matches = true
				}
			}
			if !matches {
				// The output would look like this, e.g.
				// error does not match
				// wantError error: contains substring: foo, H1, requested: secrets=8, used: secrets=4, limited: secrets=6, requested: pods=9, used: pods=3, limited: pods=4
				// got error: exceeded hierarchical quota in namespace "foo": "H1", requested: secrets=8, used: secrets=5, limited: secrets=6, requested: pods=9, used: pods=3, limited: pods=4
				return fmt.Sprintf("error does not match\n"+
					"wantError error: contains substring: %s\n"+
					"got error: %s",
					strings.TrimSuffix(expected, ", or "), err.Error())
			}
		} else {
			return fmt.Sprintf("expects no error while actual run "+
				"returns error: %s", err.Error())
		}
	}

	return ""
}

// errorMatches returns true if the actual error is consistent with the expected
// error result; otherwise, returns false.
func errorMatches(err error, r wantError) bool {
	if !strings.Contains(err.Error(), r.nm) ||
		!strings.Contains(err.Error(), r.ns) {
		return false
	}
	for _, rs := range r.exceeded {
		if !strings.Contains(err.Error(), rs.String()) {
			return false
		}
	}
	return true
}

// checkUsages checks if local resource usages of a namespace and subtree
// resource usages of the namespace and its ancestors are expected. If any of the
// resource usage is not expected, it returns false with a message with details;
// otherwise, it returns true.
func checkUsages(t *testing.T, n *Namespace, u usages) string {
	t.Helper()
	local := n.quotas.used.local
	if !utils.Equals(local, stringToResourceList(t, u[n.name])) {
		return fmt.Sprintf("want local resource usages: %v\n"+
			"got local resource usages: %v\n",
			u[n.name], local)
	}

	subtree := getSubtreeUsages(n)
	if !usagesEquals(subtree, parseUsages(t, u)) {
		return fmt.Sprintf("want subtree resource usages:\n%s"+
			"got subtree resource usages:\n%s\n",
			parseUsages(t, u),
			subtree)
	}

	return ""
}

// getSubtreeUsages returns subtree usages of the namespace and its ancestors.
func getSubtreeUsages(ns *Namespace) parsedUsages {
	var t parsedUsages
	for ns != nil {
		if len(ns.quotas.used.subtree) != 0 {
			if t == nil {
				t = parsedUsages{}
			}
			t[ns.Name()] = ns.quotas.used.subtree
		}
		ns = ns.Parent()
	}
	return t
}

// usagesEquals returns true if two usages equal; otherwise, returns false.
func usagesEquals(a parsedUsages, b parsedUsages) bool {
	if len(a) != len(b) {
		return false
	}

	if len(a) == 0 && len(b) == 0 {
		return true
	}

	for n, u := range a {
		o, ok := b[n]
		if !ok || !utils.Equals(o, u) {
			return false
		}
	}
	for n, u := range b {
		o, ok := a[n]
		if !ok || !utils.Equals(o, u) {
			return false
		}
	}
	return true
}

func parseUsages(t *testing.T, inString usages) parsedUsages {
	t.Helper()
	su := parsedUsages{}
	for nm, s := range inString {
		su[nm] = stringToResourceList(t, s)
	}
	return su
}

// resourceEquals returns true if two corev1.ResourceName slices contain the
// same elements, regardless of the order. It returns false if two slices do not
// contain the same elements.
func resourceEquals(a []corev1.ResourceName, b []corev1.ResourceName) bool {
	sort.Slice(a, func(i, j int) bool {
		return a[i] < a[j]
	})
	sort.Slice(b, func(i, j int) bool {
		return b[i] < b[j]
	})
	return reflect.DeepEqual(a, b)
}

// stringToResourceList converts a string to resource list. The input string is
// a list of resource names and quantities alternatively separated by spaces,
// e.g. "secrets 2 pods 1".
func stringToResourceList(t *testing.T, s string) corev1.ResourceList {
	t.Helper()
	if s == "" {
		return corev1.ResourceList{}
	}
	args := strings.Fields(s)
	list := map[corev1.ResourceName]resource.Quantity{}
	if len(args)%2 != 0 {
		t.Error("Need even number of arguments")
	}
	for i := 0; i < len(args); i += 2 {
		list[corev1.ResourceName(args[i])] = resource.MustParse(args[i+1])
	}
	return list
}

// parsedUsages maps a namespace name to the resource usages in the namespace.
// This type is not used in the tests, but used in the comparison functions.
type parsedUsages map[string]corev1.ResourceList

// E.g. namespace: foo; subtree: map[secrets:{{2 0} {<nil>}  DecimalSI}]
func (a parsedUsages) String() string {
	s := ""
	for n, u := range a {
		s += fmt.Sprintf("namespace: %s; subtree: %v\n", n, u)
	}
	return s
}

// For example, if the wantError is exceeding "secrets" and "pods" limits of hrq
// called "H1" in namespace "foo", it would get something like:
// foo, H1, requested: secrets=8, used: secrets=4, limited: secrets=6, requested: pods=9, used: pods=3, limited: pods=4
func (we wantError) String() string {
	msg := we.ns + ", " + we.nm + ", "
	for _, e := range we.exceeded {
		msg += e.String() + ", "
	}
	return strings.TrimSuffix(msg, ", ")
}

// For example, if the exceeded resource is "secrets", it would look like:
// requested: secrets=8, used: secrets=5, limited: secrets=6
func (e exceeded) String() string {
	return fmt.Sprintf("requested: %s=%s, used: %s=%s, limited: %s=%s",
		e.name, e.requested, e.name, e.used, e.name, e.limited)
}
