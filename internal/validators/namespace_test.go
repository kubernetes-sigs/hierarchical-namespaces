package validators

import (
	"testing"

	. "github.com/onsi/gomega"
	k8sadm "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	"sigs.k8s.io/hierarchical-namespaces/internal/config"
	"sigs.k8s.io/hierarchical-namespaces/internal/foresttest"
)

func TestDeleteSubNamespace(t *testing.T) {

	// We will put an anchor a in the namespace c if it exists.
	tests := []struct {
		name string
		// We only use the forest here to create full namespace hierarchy.
		forest string
		// The subnamespaces are created by manually adding the annotation.
		subnamespaceOf string
		fail           bool
	}{
		// There's no test case for when parent and annotation don't match, since the
		// reconciler always sets the parent to match the subnamespace-of annotation.
		// - a (subnamespace-of b)
		{name: "when parent is missing", forest: "-", subnamespaceOf: "b"},
		// - a (subnamespace-of b) -> b (no anchor)
		{name: "when anchor is missing", forest: "b-", subnamespaceOf: "b"},
		// - a (subnamespace-of c) -> c (has anchor a), b
		{name: "when annotation and anchor match", forest: "c--", subnamespaceOf: "c", fail: true},
		// - a (subnamespace-of b) -> b, c(has anchor a)
		{name: "when annotation and anchor don't match", forest: "b--", subnamespaceOf: "b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			// Create a namespace instance a and add the subnamespace-of annotation.
			sub := &corev1.Namespace{}
			sub.Name = "a"
			setSubAnnotation(sub, tc.subnamespaceOf)

			req := &nsRequest{
				ns: sub,
				op: k8sadm.Delete,
			}

			// Construct the forest
			f := foresttest.Create(tc.forest)
			// Add anchor "a" to namespace "c" if it exists. This is to test the cases
			// when the subnamespace-of annotation in "a" matches/dismatches "c".
			if f.Get("c").Exists() {
				f.Get("c").SetAnchors([]string{"a"})
			}
			vns := &Namespace{Forest: f}

			// Test
			got := vns.handle(req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}

func TestDeleteOwnerNamespace(t *testing.T) {
	f := foresttest.Create("-AA")
	vns := &Namespace{Forest: f}
	a := f.Get("a")
	aInst := &corev1.Namespace{}
	aInst.Name = "a"
	b := f.Get("b")
	c := f.Get("c")

	t.Run("Delete a namespace with subnamespaces", func(t *testing.T) {
		g := NewWithT(t)
		req := &nsRequest{
			ns: aInst,
			op: k8sadm.Delete,
		}

		// Test
		got := vns.handle(req)
		// Report - Shouldn't allow deleting the parent namespace.
		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeFalse())

		// Set allowCascadingDeletion on one child.
		b.UpdateAllowCascadingDeletion(true)
		// Test
		got = vns.handle(req)
		// Report - Still shouldn't allow deleting the parent namespace.
		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeFalse())

		// Set allowCascadingDeletion on the other child too.
		c.UpdateAllowCascadingDeletion(true)
		// Test
		got = vns.handle(req)
		// Report - Shouldn't allow deleting the parent namespace since parent namespace is not set to allow cascading deletion.
		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeFalse())

		// Unset allowCascadingDeletion on one child but set allowCascadingDeletion on the parent itself.
		c.UpdateAllowCascadingDeletion(false)
		a.UpdateAllowCascadingDeletion(true)
		// Test
		got = vns.handle(req)
		// Report - Should allow deleting the parent namespace with allowCascadingDeletion set on it.
		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeTrue())
	})
}

func TestCreateNamespace(t *testing.T) {
	// nm is the name of the namespace to be created, which already exists in external hierarchy.
	nm := "exhier"

	// Create a single external namespace "a" with "exhier" in the external hierarchy.
	f := foresttest.Create("-")
	vns := &Namespace{Forest: f}
	a := f.Get("a")
	a.ExternalTreeLabels = map[string]int{
		nm:       1,
		a.Name(): 0,
	}

	// Requested namespace uses "exhier" as name.
	ns := &corev1.Namespace{}
	ns.Name = nm

	t.Run("Create namespace with an already existing name in external hierarchy", func(t *testing.T) {
		g := NewWithT(t)
		req := &nsRequest{
			ns: ns,
			op: k8sadm.Create,
		}

		// Test
		got := vns.handle(req)

		// Report
		logResult(t, got.AdmissionResponse.Result)
		g.Expect(got.AdmissionResponse.Allowed).Should(BeFalse())
	})
}

func TestUpdateNamespaceManagedBy(t *testing.T) {
	f := foresttest.Create("-a-c") // a <- b; c <- d
	vns := &Namespace{Forest: f}

	aInst := &corev1.Namespace{}
	aInst.Name = "a"
	// Note: HC reconciler adds included-namespace label to all non-excluded
	// namespaces, so here and below we manually add the labels for this test.
	aInst.SetLabels(map[string]string{api.LabelIncludedNamespace: "true"})
	bInst := &corev1.Namespace{}
	bInst.Name = "b"
	bInst.SetLabels(map[string]string{api.LabelIncludedNamespace: "true"})

	// Add 'hnc.x-k8s.io/managed-by: other' annotation on c.
	cInst := &corev1.Namespace{}
	cInst.Name = "c"
	cInst.SetLabels(map[string]string{api.LabelIncludedNamespace: "true"})
	cInst.SetAnnotations(map[string]string{api.AnnotationManagedBy: "other"})

	// ** Please note this will make d in an *illegal* state. **
	// Add 'hnc.x-k8s.io/managed-by: other' annotation on d.
	dInst := &corev1.Namespace{}
	dInst.Name = "d"
	dInst.SetLabels(map[string]string{api.LabelIncludedNamespace: "true"})
	dInst.SetAnnotations(map[string]string{api.AnnotationManagedBy: "other"})

	// These cases test converting namespaces between internal and external, described
	// in the table at https://bit.ly/hnc-external-hierarchy#heading=h.z9mkbslfq41g
	// with other cases covered in the hierarchy_test.go.
	tests := []struct {
		name      string
		nsInst    *corev1.Namespace
		managedBy string
		fail      bool
	}{
		{name: "ok: default (no annotation)", nsInst: aInst, managedBy: ""},
		{name: "ok: explicitly managed by HNC", nsInst: aInst, managedBy: "hnc.x-k8s.io"},
		{name: "ok: convert a root internal namespace to external", nsInst: aInst, managedBy: "other"},
		{name: "not ok: convert a non-root internal namespace to external", nsInst: bInst, managedBy: "other", fail: true},
		{name: "ok: convert an external namespace to internal by changing annotation value", nsInst: cInst, managedBy: "hnc.x-k8s.io"},
		{name: "ok: convert an external namespace to internal by removing annotation", nsInst: cInst, managedBy: ""},
		{name: "ok: resolve illegal state by changing annotation value", nsInst: dInst, managedBy: "hnc.x-k8s.io"},
		{name: "ok: resolve illegal state by removing annotation", nsInst: dInst, managedBy: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			tnsInst := tc.nsInst
			if tc.managedBy == "" {
				tnsInst.SetAnnotations(map[string]string{})
			} else {
				tnsInst.SetAnnotations(map[string]string{api.AnnotationManagedBy: tc.managedBy})
			}

			req := &nsRequest{
				ns: tc.nsInst,
				op: k8sadm.Update,
			}

			// Test
			got := vns.handle(req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}

// setSubAnnotations sets subnamespace-of annotation with a parent name on the
// namespace. If the parent name is empty, it removes the annotation.
func setSubAnnotation(ns *corev1.Namespace, pnm string) {
	a := make(map[string]string)
	if pnm != "" {
		a[api.SubnamespaceOf] = pnm
	}
	ns.SetAnnotations(a)
}

func TestIllegalIncludedNamespaceNamespace(t *testing.T) {
	f := foresttest.Create("-a-c") // a <- b; c <- d
	vns := &Namespace{Forest: f}
	config.ExcludedNamespaces["excluded"] = true

	tests := []struct {
		name       string
		excludedNS bool
		oldlval    string
		lval       string
		op         k8sadm.Operation
		fail       bool
	}{
		// Note: for all the test cases below, you can consider without label as 0,
		// with correct label as 1 and with wrong label as 2, so the test cases are
		// generated as 0->0, 0->1, 0->2; 1->0, 1->1, 1->2; 2->0, 2->1, 2->2. The
		// test cases are divided into 3 blocks - Create, Update and Delete. There's
		// no old namespace instance for Create and there's no new namespace instance
		// for Delete.

		// Create operations on included namespaces.
		{name: "deny creating included namespace without label (this case should never happen in reality since mutator adds the correct label)", op: k8sadm.Create, fail: true},
		{name: "allow creating included namespace with correct label (e.g. from a yaml file or added by mutator)", lval: "true", op: k8sadm.Create},
		{name: "deny creating included namespace with wrong label(e.g. from a yaml file)", lval: "wrong", op: k8sadm.Create, fail: true},

		// Create operations on excluded namespaces.
		{name: "allow creating excluded namespace without label", excludedNS: true, op: k8sadm.Create},
		{name: "deny creating excluded namespace with correct label", excludedNS: true, lval: "true", op: k8sadm.Create, fail: true},
		{name: "deny creating excluded namespace with wrong label", excludedNS: true, lval: "wrong", op: k8sadm.Create, fail: true},

		// Update operations on included namespaces.
		{name: "allow other updates on included namespace without label (this case should never happen with mutator adding the correct label)", op: k8sadm.Update},
		{name: "allow adding correct label to included namespace", lval: "true", op: k8sadm.Update},
		{name: "deny adding wrong label to included namespace", lval: "wrong", op: k8sadm.Update, fail: true},

		{name: "deny removing the correct label from included namespace (this case should never happen in reality since mutator adds the correct label)", oldlval: "true", op: k8sadm.Update, fail: true},
		{name: "allow other updates on included namespace with correct label or removing the correct label (mutator adds it back)", oldlval: "true", lval: "true", op: k8sadm.Update},
		{name: "deny updating included namespace to have wrong label", oldlval: "true", lval: "wrong", op: k8sadm.Update, fail: true},

		{name: "deny removing wrong label from included namespace (this case should never happen in reality since mutator adds the correct label)", oldlval: "wrong", op: k8sadm.Update, fail: true},
		{name: "allow updating included namespace to have correct label or just removing wrong label (mutator adds correct label)", oldlval: "wrong", lval: "true", op: k8sadm.Update},
		{name: "allow other updates on included namespace even with wrong label", oldlval: "wrong", lval: "wrong", op: k8sadm.Update},
		{name: "deny updating included namespace wrong label to another wrong label", oldlval: "wrong1", lval: "wrong2", op: k8sadm.Update, fail: true},

		// Update operations on excluded namespaces.
		{name: "allow other updates on excluded namespace without label", excludedNS: true, op: k8sadm.Update},
		{name: "deny adding illegal label (with correct value) to excluded namespace", excludedNS: true, lval: "true", op: k8sadm.Update, fail: true},
		{name: "deny adding illegal label (with wrong value) to excluded namespace", excludedNS: true, lval: "wrong", op: k8sadm.Update, fail: true},

		{name: "allow removing illegal label (with correct value) from excluded namespace", excludedNS: true, oldlval: "true", op: k8sadm.Update},
		{name: "allow other updates on excluded namespace with illegal label (with correct value)", excludedNS: true, oldlval: "true", lval: "true", op: k8sadm.Update},
		{name: "deny updating illegal label value (correct to wrong value) on excluded namespace", excludedNS: true, oldlval: "true", lval: "wrong", op: k8sadm.Update, fail: true},

		{name: "allow removing illegal label (with wrong value) from excluded namespace", excludedNS: true, oldlval: "wrong", op: k8sadm.Update},
		{name: "deny updating illegal label value (wrong to correct value) on excluded namespace", excludedNS: true, oldlval: "wrong", lval: "true", op: k8sadm.Update, fail: true},
		{name: "allow other updates on excluded namespace with illegal label (with wrong value)", excludedNS: true, oldlval: "wrong", lval: "wrong", op: k8sadm.Update},
		{name: "deny updating illegal label value (wrong to another wrong value) on excluded namespace", excludedNS: true, oldlval: "wrong1", lval: "wrong2", op: k8sadm.Update, fail: true},

		// Delete operations on included namespaces.
		{name: "allow deleting included namespace without label", op: k8sadm.Delete},
		{name: "allow deleting included namespace with label", oldlval: "true", op: k8sadm.Delete},
		{name: "allow deleting included namespace with wrong label", oldlval: "wrong", op: k8sadm.Delete},

		// Delete operations on excluded namespaces.
		{name: "allow deleting excluded namespace without label", excludedNS: true, op: k8sadm.Delete},
		{name: "allow deleting excluded namespace with label", excludedNS: true, oldlval: "true", op: k8sadm.Delete},
		{name: "allow deleting excluded namespace with wrong label", excludedNS: true, oldlval: "wrong", op: k8sadm.Delete},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			nnm := "a"
			if tc.excludedNS {
				nnm = "excluded"
			}

			ns := &corev1.Namespace{}
			ns.Name = nnm
			if tc.lval != "" {
				ns.SetLabels(map[string]string{api.LabelIncludedNamespace: tc.lval})
			}
			oldns := &corev1.Namespace{}
			if tc.op == k8sadm.Update {
				oldns.Name = nnm
				if tc.oldlval != "" {
					oldns.SetLabels(map[string]string{api.LabelIncludedNamespace: tc.oldlval})
				}
			} else {
				oldns = nil
			}

			req := &nsRequest{
				oldns: oldns,
				ns:    ns,
				op:    tc.op,
			}

			// Test
			got := vns.handle(req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}
