//go:build windows

package executor

import (
	"errors"

	"setpoint/internal/trustedexec"
)

func resolveSecureExecutable(_ string, builtInRoots []string, approvedRoots []trustedexec.Root) (string, error) {
	return "", errors.Join(ErrCommandNotFound, ErrTrustedRootUnsupported)
}
