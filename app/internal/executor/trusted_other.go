//go:build !linux && !windows

package executor

import "setpoint/internal/trustedexec"

func resolveSecureExecutable(_ string, _ []string, _ []trustedexec.Root) (string, error) {
	return "", ErrTrustedRootUnsupported
}
