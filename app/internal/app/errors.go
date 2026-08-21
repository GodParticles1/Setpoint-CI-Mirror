package app

import "errors"

type ValidationError struct {
	Err error
}

func (err *ValidationError) Error() string {
	return err.Err.Error()
}

func (err *ValidationError) Unwrap() error {
	return err.Err
}

func IsValidationError(err error) bool {
	var validationError *ValidationError
	return errors.As(err, &validationError)
}
