package app

import "errors"

type ConflictError struct {
	Err error
}

func (err *ConflictError) Error() string {
	return err.Err.Error()
}

func (err *ConflictError) Unwrap() error {
	return err.Err
}

func IsConflictError(err error) bool {
	var conflict *ConflictError
	return errors.As(err, &conflict)
}
