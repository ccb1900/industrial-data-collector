//go:build !windows

package state

func renameRetryable(error) bool { return false }
