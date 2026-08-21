package auth

import "fmt"

type ErrorCode string

const (
	CodeMissing                  ErrorCode = "auth_missing"
	CodeMalformed                ErrorCode = "auth_malformed"
	CodeInvalid                  ErrorCode = "auth_invalid"
	CodeExpired                  ErrorCode = "auth_expired"
	CodeRevoked                  ErrorCode = "auth_revoked"
	CodeAgentMismatch            ErrorCode = "auth_agent_mismatch"
	CodeEnrollmentTokenExpired   ErrorCode = "enrollment_token_expired"
	CodeEnrollmentTokenRevoked   ErrorCode = "enrollment_token_revoked"
	CodeEnrollmentTokenExhausted ErrorCode = "enrollment_token_exhausted"
)

type Error struct {
	Code ErrorCode
	Err  error
}

func (err *Error) Error() string {
	if err.Err == nil {
		return string(err.Code)
	}
	return fmt.Sprintf("%s: %v", err.Code, err.Err)
}

func (err *Error) Unwrap() error {
	return err.Err
}
