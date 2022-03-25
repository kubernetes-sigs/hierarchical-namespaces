package config

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

func ValidateManagedLabels(labels []api.MetaKVP) field.ErrorList {
	allErrs := field.ErrorList{}

	fldPath := field.NewPath("spec", "labels")
	for _, l := range labels {
		if fldErr := validateMetaKey(l.Key, fldPath); fldErr != nil {
			allErrs = append(allErrs, fldErr)
		} else if !IsManagedLabel(l.Key) { // Only validate managed if key is valid
			fldErr := field.Invalid(fldPath, l.Key, "not a managed label and cannot be configured")
			allErrs = append(allErrs, fldErr)
		}
		if fldErr := validateLabelValue(l.Key, l.Value, fldPath); fldErr != nil {
			allErrs = append(allErrs, fldErr)
		}
	}
	return allErrs
}

func ValidateManagedAnnotations(annotations []api.MetaKVP) field.ErrorList {
	allErrs := field.ErrorList{}

	fldPath := field.NewPath("spec", "annotations")
	for _, a := range annotations {
		if fldErr := validateMetaKey(a.Key, fldPath); fldErr != nil {
			allErrs = append(allErrs, fldErr)
		} else if !IsManagedAnnotation(a.Key) { // Only validate managed if key is valid
			fldErr := field.Invalid(fldPath, a.Key, "not a managed annotation and cannot be configured")
			allErrs = append(allErrs, fldErr)
		}
		// No validation of annotation values; only limitation seems to be the total length (256Kb) of all annotations
	}
	return allErrs
}

func validateMetaKey(k string, path *field.Path) *field.Error {
	if errs := validation.IsQualifiedName(k); len(errs) != 0 {
		return field.Invalid(path, k, strings.Join(errs, "; "))
	}
	return nil
}

func validateLabelValue(k, v string, path *field.Path) *field.Error {
	if errs := validation.IsValidLabelValue(v); len(errs) != 0 {
		return field.Invalid(path.Key(k), v, strings.Join(errs, "; "))
	}
	return nil
}
