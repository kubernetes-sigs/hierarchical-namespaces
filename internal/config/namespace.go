package config

import (
	"fmt"
	"regexp"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

var (
	// excludedNamespaces is a list of namespaces used by reconcilers and validators
	// to exclude namespaces that shouldn't be reconciled or validated.
	//
	// This value is controlled by the --excluded-namespace command line, which may
	// be set multiple times.
	excludedNamespaces map[string]bool

	// includedNamespacesRegex is the compiled regex of included namespaces
	includedNamespacesRegex *regexp.Regexp

	// includedNamespacesStr is the original pattern of the regex. It must
	// only be used to generate error messages.
	includedNamespacesStr string

	// managedLabels is the list of compiled regexes for managed labels. Any label in this list is
	// removed from all managed namespaces unless specifically specified by the HC of the namespace or
	// one of its ancestors.
	managedLabels []*regexp.Regexp

	// managedAnnotations is the list of compiled regexes for managed annotations. Any annotations in
	// this list is removed from all managed namespaces unless specifically specified by the HC of the
	// namespace or one of its ancestors.
	managedAnnotations []*regexp.Regexp
)

func SetNamespaces(regex string, excluded ...string) {
	if regex == "" {
		regex = ".*"
	}

	includedNamespacesStr = "^" + regex + "$"
	includedNamespacesRegex = regexp.MustCompile(includedNamespacesStr)

	excludedNamespaces = make(map[string]bool)
	for _, exn := range excluded {
		excludedNamespaces[exn] = true
	}
}

// WhyUnamanged returns a human-readable message explaining why the given
// namespace is unmanaged, or an empty string if it *is* managed.
func WhyUnmanaged(nm string) string {
	// We occasionally check if the _parent_ of a namespace is managed.
	// It's an error for a managed namespace to have an unmanaged parent,
	// so we'll treat "no parent" as managed.
	if nm == "" {
		return ""
	}

	if excludedNamespaces[nm] {
		return "excluded by the HNC administrator"
	}

	if includedNamespacesRegex == nil { // unit tests
		return ""
	}

	if !includedNamespacesRegex.MatchString(nm) {
		return "does not match the regex set by the HNC administrator: `" + includedNamespacesStr + "`"
	}

	return ""
}

// IsManagedNamespace is the same as WhyUnmanaged but converts the response to a bool for convenience.
func IsManagedNamespace(nm string) bool {
	return WhyUnmanaged(nm) == ""
}

// SetManagedMeta sets the regexes for the managed namespace labels and annotations. The function
// ensures that all strings are valid regexes, and that they do not attempt to select for HNC
// metadata.
func SetManagedMeta(labels, annots []string) error {
	if err := setManagedMeta(labels, "--managed-namespace-label", &managedLabels); err != nil {
		return err
	}
	if err := setManagedMeta(annots, "--managed-namespace-annotation", &managedAnnotations); err != nil {
		return err
	}
	return nil
}

func setManagedMeta(patterns []string, option string, regexes *[]*regexp.Regexp) error {
	// Reset (useful for unit tests)
	*regexes = nil

	// Validate regexes
	for _, p := range patterns {
		r, err := regexp.Compile("^" + p + "$")
		if err != nil {
			return fmt.Errorf("illegal value for %s %q: %w", option, p, err)
		}
		if r.MatchString(api.MetaGroup) {
			return fmt.Errorf("illegal value for %s %q: cannot specify a pattern that matches %q", option, p, api.MetaGroup)
		}
		*regexes = append(*regexes, r)
	}
	return nil
}

func IsManagedLabel(k string) bool {
	for _, regex := range managedLabels {
		if regex.MatchString(k) {
			return true
		}
	}
	return false
}

func IsManagedAnnotation(k string) bool {
	for _, regex := range managedAnnotations {
		if regex.MatchString(k) {
			return true
		}
	}
	return false
}
