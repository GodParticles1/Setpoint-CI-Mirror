//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

func reexecAfterEnrollment() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Agent executable after enrollment: %w", err)
	}
	arguments := append([]string{executable}, os.Args[1:]...)
	if err := syscall.Exec(executable, arguments, environmentWithoutEnrollmentToken(os.Environ())); err != nil {
		return fmt.Errorf("re-exec Agent after enrollment: %w", err)
	}
	return nil
}
