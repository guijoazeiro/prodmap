package identity

import (
	"fmt"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

// ValidEnvironment validates the shared service-identity environment.
func ValidEnvironment(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "", fmt.Errorf("%w: environment must contain 1..128 characters", errs.ErrInvalid)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", fmt.Errorf("%w: environment contains control characters", errs.ErrInvalid)
		}
	}
	return value, nil
}
