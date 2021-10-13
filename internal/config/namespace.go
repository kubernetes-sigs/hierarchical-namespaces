package config

import (
	"regexp"
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
