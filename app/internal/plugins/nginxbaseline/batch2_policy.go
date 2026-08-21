package nginxbaseline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

type corsPolicy struct {
	Configured bool
	Allowed    map[string]struct{}
}

func parseCORSPolicy(raw json.RawMessage) (corsPolicy, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return corsPolicy{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil {
		return corsPolicy{}, errors.New("decode Nginx parameters: expected a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return corsPolicy{}, errors.New("decode Nginx parameters: trailing JSON value")
	}
	for name := range values {
		if name != corsAllowedOriginsParameter {
			return corsPolicy{}, fmt.Errorf("unknown Nginx parameter %q", name)
		}
	}
	encoded, exists := values[corsAllowedOriginsParameter]
	if !exists {
		return corsPolicy{}, nil
	}
	var configured string
	if err := json.Unmarshal(encoded, &configured); err != nil {
		return corsPolicy{}, errors.New("cors_allowed_origins must be a string")
	}
	parts := strings.Split(configured, ",")
	allowed := make(map[string]struct{}, len(parts))
	for index, part := range parts {
		origin, err := normalizeOrigin(part)
		if err != nil {
			return corsPolicy{}, fmt.Errorf("cors_allowed_origins entry %d is invalid: %w", index+1, err)
		}
		if _, duplicate := allowed[origin]; duplicate {
			return corsPolicy{}, fmt.Errorf("cors_allowed_origins entry %d duplicates an earlier origin", index+1)
		}
		allowed[origin] = struct{}{}
	}
	if len(allowed) == 0 {
		return corsPolicy{}, errors.New("cors_allowed_origins must contain at least one origin")
	}
	return corsPolicy{Configured: true, Allowed: allowed}, nil
}

func normalizeOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "*" {
		return "", errors.New("origin must be a non-wildcard absolute HTTP/HTTPS origin")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return "", errors.New("origin must be an absolute HTTP/HTTPS origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("origin scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("origin must not contain credentials, path, query, or fragment")
	}
	return scheme + "://" + strings.ToLower(parsed.Host), nil
}
