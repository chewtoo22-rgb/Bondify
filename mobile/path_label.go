package mobile

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxPathLabelBytes = 64

// validatePathLabel keeps Android's human-readable physical-network identity bounded and
// unambiguous before a socket is adopted or a path is registered. Labels are local control
// identifiers, not free-form UI text.
func validatePathLabel(label string) error {
	if label == "" {
		return fmt.Errorf("mobile: path label must not be empty")
	}
	if !utf8.ValidString(label) {
		return fmt.Errorf("mobile: path label must be valid UTF-8")
	}
	if len(label) > maxPathLabelBytes {
		return fmt.Errorf("mobile: path label exceeds %d bytes", maxPathLabelBytes)
	}
	if strings.TrimSpace(label) != label {
		return fmt.Errorf("mobile: path label must not have leading or trailing whitespace")
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return fmt.Errorf("mobile: path label contains control characters")
		}
	}
	return nil
}

func pathLabelExists(labels []string, label string) bool {
	for _, existing := range labels {
		if existing == label {
			return true
		}
	}
	return false
}
