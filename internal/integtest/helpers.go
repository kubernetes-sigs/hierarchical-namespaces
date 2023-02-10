package integtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2" //lint:ignore ST1001 Ignoring this for now
	. "github.com/onsi/gomega"    //lint:ignore ST1001 Ignoring this for now
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

// GVKs maps a resource to its corresponding GVK.
var GVKs = map[string]schema.GroupVersionKind{
	"secrets":               {Group: "", Version: "v1", Kind: "Secret"},
	api.RoleResource:        {Group: api.RBACGroup, Version: "v1", Kind: api.RoleKind},
	api.RoleBindingResource: {Group: api.RBACGroup, Version: "v1", Kind: api.RoleBindingKind},
	"networkpolicies":       {Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"},
	"resourcequotas":        {Group: "", Version: "v1", Kind: "ResourceQuota"},
	"limitranges":           {Group: "", Version: "v1", Kind: "LimitRange"},
	"configmaps":            {Group: "", Version: "v1", Kind: "ConfigMap"},
	// crontabs is a custom resource.
	"crontabs":            {Group: "stable.example.com", Version: "v1", Kind: "CronTab"},
	"subnamespaceanchors": {Group: "hnc.x-k8s.io", Version: "v1alpha2", Kind: "SubnamespaceAnchor"},
}

// createdObjects keeps track of objects created out of the MakeObject function.
// This gives us a reflection of what's stored in the API server so that we can
// clean it up properly when CleanupObjects is called.
var createdObjects = []*unstructured.Unstructured{}

func NewHierarchy(nm string) *api.HierarchyConfiguration {
	hier := &api.HierarchyConfiguration{}
	hier.ObjectMeta.Namespace = nm
	hier.ObjectMeta.Name = api.Singleton
	return hier
}

func GetHierarchy(ctx context.Context, nm string) *api.HierarchyConfiguration {
	nnm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
	hier := &api.HierarchyConfiguration{}
	if err := K8sClient.Get(ctx, nnm, hier); err != nil {
		GinkgoT().Logf("Error fetching hierarchy for %s: %s", nm, err)
	}
	return hier
}

func CanGetHierarchy(ctx context.Context, nm string) func() bool {
	return func() bool {
		nnm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
		hier := &api.HierarchyConfiguration{}
		if err := K8sClient.Get(ctx, nnm, hier); err != nil {
			return false
		}
		return true
	}
}

func UpdateHierarchy(ctx context.Context, h *api.HierarchyConfiguration) {
	if h.CreationTimestamp.IsZero() {
		ExpectWithOffset(1, K8sClient.Create(ctx, h)).Should(Succeed())
	} else {
		ExpectWithOffset(1, K8sClient.Update(ctx, h)).Should(Succeed())
	}
}

func TryUpdateHierarchy(ctx context.Context, h *api.HierarchyConfiguration) error {
	if h.CreationTimestamp.IsZero() {
		return K8sClient.Create(ctx, h)
	} else {
		return K8sClient.Update(ctx, h)
	}
}

func GetLabel(ctx context.Context, from, label string) func() string {
	return func() string {
		ns := GetNamespace(ctx, from)
		val := ns.GetLabels()[label]
		return val
	}
}

func GetAnnotation(ctx context.Context, from, key string) func() string {
	return func() string {
		ns := GetNamespace(ctx, from)
		val := ns.GetAnnotations()[key]
		return val
	}
}

func HasChild(ctx context.Context, nm, cnm string) func() bool {
	return func() bool {
		children := GetHierarchy(ctx, nm).Status.Children
		for _, c := range children {
			if c == cnm {
				return true
			}
		}
		return false
	}
}

// Namespaces are named "a-<rand>", "b-<rand>", etc
func CreateNSes(ctx context.Context, num int) []string {
	nms := []string{}
	for i := 0; i < num; i++ {
		nm := CreateNS(ctx, fmt.Sprintf("%c", 'a'+i))
		nms = append(nms, nm)
	}
	return nms
}

func AddNamespaceLabel(ctx context.Context, nm, k, v string) {
	ns := GetNamespace(ctx, nm)
	l := ns.Labels
	l[k] = v
	ns.SetLabels(l)
	UpdateNamespace(ctx, ns)
}

func RemoveNamespaceLabel(ctx context.Context, nm, k string) {
	ns := GetNamespace(ctx, nm)
	l := ns.Labels
	delete(l, k)
	ns.SetLabels(l)
	UpdateNamespace(ctx, ns)
}

func UpdateNamespace(ctx context.Context, ns *corev1.Namespace) {
	ExpectWithOffset(1, K8sClient.Update(ctx, ns)).Should(Succeed())
}

func SetParent(ctx context.Context, nm, pnm string) {
	var oldPNM string
	GinkgoT().Logf("Changing parent of %s to %s", nm, pnm)
	EventuallyWithOffset(1, func() error {
		hier := NewOrGetHierarchy(ctx, nm)
		oldPNM = hier.Spec.Parent
		hier.Spec.Parent = pnm
		return TryUpdateHierarchy(ctx, hier) // can fail if a reconciler updates the hierarchy
	}).Should(Succeed(), "When setting parent of %s to %s", nm, pnm)
	if oldPNM != "" {
		EventuallyWithOffset(1, func() []string {
			pHier := GetHierarchy(ctx, oldPNM)
			return pHier.Status.Children
		}).ShouldNot(ContainElement(nm), "Verifying %s is no longer a child of %s", nm, oldPNM)
	}
	if pnm != "" {
		EventuallyWithOffset(1, func() []string {
			pHier := GetHierarchy(ctx, pnm)
			return pHier.Status.Children
		}).Should(ContainElement(nm), "Verifying %s is now a child of %s", nm, pnm)
	}
}

func GetNamespace(ctx context.Context, nm string) *corev1.Namespace {
	return GetNamespaceWithOffset(1, ctx, nm)
}

func GetNamespaceWithOffset(offset int, ctx context.Context, nm string) *corev1.Namespace {
	nnm := types.NamespacedName{Name: nm}
	ns := &corev1.Namespace{}
	EventuallyWithOffset(offset+1, func() error {
		return K8sClient.Get(ctx, nnm, ns)
	}).Should(Succeed())
	return ns
}

// CreateNS is a convenience function to create a namespace and wait for its singleton to be
// created. It's used in other tests in this package, but basically duplicates the code in this test
// (it didn't originally). TODO: refactor.
func CreateNS(ctx context.Context, prefix string) string {
	nm := CreateNSName(prefix)

	// Create the namespace
	ns := &corev1.Namespace{}
	ns.Name = nm
	Expect(K8sClient.Create(ctx, ns)).Should(Succeed())
	return nm
}

// CreateNSName generates random namespace names. Namespaces are never deleted in test-env because
// the building Namespace controller (which finalizes namespaces) doesn't run; I searched Github and
// found that everyone who was deleting namespaces was *also* very intentionally generating random
// names, so I guess this problem is widespread.
func CreateNSName(prefix string) string {
	suffix := make([]byte, 10)
	rand.Read(suffix)
	return fmt.Sprintf("%s-%x", prefix, suffix)
}

// CreateNSWithLabel has similar function to CreateNS with label as additional parameter
func CreateNSWithLabel(ctx context.Context, prefix string, label map[string]string) string {
	nm := CreateNSName(prefix)

	// Create the namespace
	ns := &corev1.Namespace{}
	ns.SetLabels(label)
	ns.Name = nm
	Expect(K8sClient.Create(ctx, ns)).Should(Succeed())
	return nm
}

// CreateNSWithLabelAnnotation has similar function to CreateNS with label and annotation
// as additional parameters.
func CreateNSWithLabelAnnotation(ctx context.Context, prefix string, l map[string]string, a map[string]string) string {
	nm := CreateNSName(prefix)

	// Create the namespace
	ns := &corev1.Namespace{}
	ns.SetLabels(l)
	ns.SetAnnotations(a)
	ns.Name = nm
	Expect(K8sClient.Create(ctx, ns)).Should(Succeed())
	return nm
}

func UpdateHNCConfig(ctx context.Context, c *api.HNCConfiguration) error {
	if c.CreationTimestamp.IsZero() {
		if err := K8sClient.Create(ctx, c); err != nil {
			return fmt.Errorf("while creating HNCConfiguration %q: %w", c.GetName(), err)
		}
	} else {
		if err := K8sClient.Update(ctx, c); err != nil {
			return fmt.Errorf("while updating HNCConfiguration %q: %w", c.GetName(), err)
		}
	}
	return nil
}

func ResetHNCConfigToDefault(ctx context.Context) {
	EventuallyWithOffset(1, func() error {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return err
		}
		c.Spec = api.HNCConfigurationSpec{}
		c.Status = api.HNCConfigurationStatus{}
		return K8sClient.Update(ctx, c)
	}).Should(Succeed(), "While resetting HNC config")
}

func GetHNCConfig(ctx context.Context) (*api.HNCConfiguration, error) {
	return GetHNCConfigWithName(ctx, api.HNCConfigSingleton)
}

func GetHNCConfigWithName(ctx context.Context, nm string) (*api.HNCConfiguration, error) {
	nnm := types.NamespacedName{Name: nm}
	config := &api.HNCConfiguration{}
	if err := K8sClient.Get(ctx, nnm, config); err != nil {
		return nil, fmt.Errorf("while reading HNCConfiguration %q: %w", nm, err)
	}
	return config, nil
}

func AddToHNCConfig(ctx context.Context, group, resource string, mode api.SynchronizationMode) {
	EventuallyWithOffset(1, func() error {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return err
		}
		spec := api.ResourceSpec{Group: group, Resource: resource, Mode: mode}
		c.Spec.Resources = append(c.Spec.Resources, spec)
		return UpdateHNCConfig(ctx, c)
	}).Should(Succeed(), "While adding %s/%s=%s to HNC config", group, resource, mode)
}

// HasObject returns true if a namespace contains a specific object of the given kind.
//
//	The kind and its corresponding GVK should be included in the GVKs map.
func HasObject(ctx context.Context, resource string, nsName, name string) func() bool {
	// `Eventually` only works with a fn that doesn't take any args.
	return func() bool {
		_, err := GetObject(ctx, resource, nsName, name)
		return err == nil
	}
}

func GetObject(ctx context.Context, resource string, nsName, name string) (*unstructured.Unstructured, error) {
	nnm := types.NamespacedName{Namespace: nsName, Name: name}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	err := K8sClient.Get(ctx, nnm, inst)
	return inst, err
}

// MakeObject creates an empty object of the given kind in a specific namespace. The kind and
// its corresponding GVK should be included in the GVKs map.
func MakeObject(ctx context.Context, resource string, nsName, name string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	ExpectWithOffset(1, K8sClient.Create(ctx, inst)).Should(Succeed(), "When creating %s %s/%s", resource, nsName, name)
	createdObjects = append(createdObjects, inst)
}

// MakeObjectWithAnnotations creates an empty object with annotation given kind in a specific
// namespace. The kind and its corresponding GVK should be included in the GVKs map.
func MakeObjectWithAnnotations(ctx context.Context, resource string, nsName,
	name string, a map[string]string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	inst.SetAnnotations(a)
	ExpectWithOffset(1, K8sClient.Create(ctx, inst)).Should(Succeed())
	createdObjects = append(createdObjects, inst)
}

// MakeObjectWithLabels creates an empty object with label given kind in a specific
// namespace. The kind and its corresponding GVK should be included in the GVKs map.
func MakeObjectWithLabels(ctx context.Context, resource string, nsName,
	name string, l map[string]string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	inst.SetLabels(l)
	ExpectWithOffset(1, K8sClient.Create(ctx, inst)).Should(Succeed())
	createdObjects = append(createdObjects, inst)
}

// MakeSecrettWithType creates an empty Secret with type given kind in a specific namespace.
func MakeSecrettWithType(ctx context.Context, nsName, name, scType string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs["secrets"])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	inst.UnstructuredContent()["type"] = scType
	ExpectWithOffset(1, K8sClient.Create(ctx, inst)).Should(Succeed())
	createdObjects = append(createdObjects, inst)
}

// UpdateObjectWithAnnotations gets an object given it's kind, nsName and name, adds the annotation
// and updates this object
func UpdateObjectWithAnnotations(ctx context.Context, resource, nsName, name string, a map[string]string) {
	nnm := types.NamespacedName{Namespace: nsName, Name: name}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	err := K8sClient.Get(ctx, nnm, inst)
	Expect(err).ShouldNot(HaveOccurred())

	inst.SetAnnotations(a)
	err = K8sClient.Update(ctx, inst)
	Expect(err).ShouldNot(HaveOccurred())
}

// DeleteObject deletes an object of the given kind in a specific namespace. The kind and
// its corresponding GVK should be included in the GVKs map.
func DeleteObject(ctx context.Context, resource string, nsName, name string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	EventuallyWithOffset(1, func() bool {
		return errors.IsNotFound(K8sClient.Delete(ctx, inst))
	}).Should(BeTrue())
}

// CleanupObjects makes a best attempt to cleanup all objects created from MakeObject.
func CleanupObjects(ctx context.Context) {
	for _, obj := range createdObjects {
		err := K8sClient.Delete(ctx, obj)
		if err != nil {
			Eventually(errors.IsNotFound(K8sClient.Delete(ctx, obj))).Should(BeTrue())
		}
	}
	createdObjects = []*unstructured.Unstructured{}
}

// ObjectInheritedFrom returns the name of the namespace where a specific object of a given kind
// is propagated from or an empty string if the object is not a propagated object. The kind and
// its corresponding GVK should be included in the GVKs map.
func ObjectInheritedFrom(ctx context.Context, resource string, nsName, name string) string {
	nnm := types.NamespacedName{Namespace: nsName, Name: name}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	if err := K8sClient.Get(ctx, nnm, inst); err != nil {
		// should have been caught above
		return err.Error()
	}
	if inst.GetLabels() == nil {
		return ""
	}
	lif := inst.GetLabels()[api.LabelInheritedFrom]
	return lif
}

// ReplaceStrings returns a copy of str with all non-overlapping instances of the keys in table
// replaced by values in table
func ReplaceStrings(str string, table map[string]string) string {
	for key, val := range table {
		str = strings.ReplaceAll(str, key, val)
	}
	return str
}

func NewOrGetHierarchy(ctx context.Context, nm string) *api.HierarchyConfiguration {
	hier := &api.HierarchyConfiguration{}
	hier.ObjectMeta.Namespace = nm
	hier.ObjectMeta.Name = api.Singleton
	snm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
	if err := K8sClient.Get(ctx, snm, hier); err != nil {
		ExpectWithOffset(2, errors.IsNotFound(err)).Should(BeTrue())
	}
	return hier
}

func HasCondition(ctx context.Context, nm string, tp, reason string) func() bool {
	return func() bool {
		conds := GetHierarchy(ctx, nm).Status.Conditions
		for _, cond := range conds {
			if cond.Type == tp && cond.Reason == reason {
				return true
			}
		}
		return false
	}
}
