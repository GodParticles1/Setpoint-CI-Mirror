package executor

import "errors"

var (
	ErrCommandNotFound        = errors.New("command was not found in trusted executable roots")
	ErrCommandAmbiguous       = errors.New("multiple distinct trusted executables matched the command")
	ErrExecutableUntrusted    = errors.New("executable failed trusted-root validation")
	ErrTrustedRootInvalid     = errors.New("trusted executable root is invalid")
	ErrTrustedRootUnsupported = errors.New("trusted executable root validation is unsupported on this platform")
)
