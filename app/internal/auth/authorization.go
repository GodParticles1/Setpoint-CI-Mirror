package auth

import "strings"

func ParseBearer(value string, kind TokenKind) (PresentedToken, error) {
	if strings.TrimSpace(value) == "" {
		return PresentedToken{}, &Error{Code: CodeMissing}
	}
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return PresentedToken{}, &Error{Code: CodeMalformed}
	}
	presented, err := Parse(kind, parts[1])
	if err != nil {
		return PresentedToken{}, &Error{Code: CodeMalformed, Err: err}
	}
	return presented, nil
}
