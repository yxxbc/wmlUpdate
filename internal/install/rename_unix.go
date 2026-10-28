//go:build !windows

package install

func retryableRenameError(error) bool { return false }
