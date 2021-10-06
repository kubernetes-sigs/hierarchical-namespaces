package config

import (
	"regexp"
)

func SetNamespaces(regex string, excluded ...string) {

	if regex == "" {
		regex = ".*"
	}

	includedNamespacesRegex = regexp.MustCompile("^" + regex + "$")

	excludedNamespaces = make(map[string]bool)
	for _, exn := range excluded {
		excludedNamespaces[exn] = true
	}

}

func IsNamespaceIncluded(name string) bool {

	if excludedNamespaces[name] {
		return false
	}

	return includedNamespacesRegex.MatchString(name)

}
