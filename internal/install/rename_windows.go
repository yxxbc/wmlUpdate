//go:build windows

package install

import (
	"errors"

	"golang.org/x/sys/windows"
)

func retryableRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
