//go:build !linux

package proclife

// DieWithParent is a no-op on non-Linux platforms — PR_SET_PDEATHSIG is
// Linux-specific. See proclife_linux.go for the real implementation.
func DieWithParent() error { return nil }
