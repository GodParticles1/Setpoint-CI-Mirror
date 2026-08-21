//go:build !linux

package main

func reexecAfterEnrollment() error {
	return nil
}
