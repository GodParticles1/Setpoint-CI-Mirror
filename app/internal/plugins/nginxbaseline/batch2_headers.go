package nginxbaseline

import (
	"fmt"
	"strings"
	"time"

	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

type securityHeaderSpec struct {
	definition checkutil.Definition
	name       string
	valid      func(string) bool
}

func securityHeaderSpecs() []securityHeaderSpec {
	exact := func(expected string) func(string) bool {
		return func(value string) bool { return strings.EqualFold(normalizedHeaderValue(value), expected) }
	}
	return []securityHeaderSpec{
		{batch2Definitions[1], "X-Frame-Options", func(value string) bool {
			value = normalizedHeaderValue(value)
			return strings.EqualFold(value, "SAMEORIGIN") || strings.EqualFold(value, "DENY")
		}},
		{batch2Definitions[2], "X-XSS-Protection", exact("1; mode=block")},
		{batch2Definitions[3], "X-Content-Type-Options", exact("nosniff")},
		{batch2Definitions[4], "Referrer-Policy", exact("no-referrer-when-downgrade")},
	}
}

func securityHeaderItem(configuration parsedConfig, spec securityHeaderSpec, now time.Time) task.CheckItem {
	contexts := httpEvaluationContexts(configuration)
	if len(contexts) == 0 {
		if httpConfigAmbiguous(configuration) {
			return checkutil.ManualReview(spec.definition, "no directly scoped HTTP context",
				"Includes or detached server blocks prevent reconstructing the effective HTTP scope",
				"No complete server/location inheritance chain was available", now)
		}
		return checkutil.NotApplicable(spec.definition, "No HTTP server or location block was directly observed", now)
	}
	safe, missing, invalid, dynamic := 0, 0, 0, 0
	for _, context := range contexts {
		headers := effectiveDirectives(configuration.Directives, "add_header", context)
		matches := matchingHeaders(headers, spec.name)
		if len(matches) == 0 {
			missing++
			continue
		}
		contextSafe := true
		for _, header := range matches {
			value, always, isDynamic := headerValue(header)
			if isDynamic {
				dynamic++
				contextSafe = false
				continue
			}
			if !always || !spec.valid(value) {
				invalid++
				contextSafe = false
			}
		}
		if contextSafe {
			safe++
		}
	}
	current := fmt.Sprintf("%d context(s): %d covered, %d missing, %d invalid, %d dynamic",
		len(contexts), safe, missing, invalid, dynamic)
	ambiguous := httpConfigAmbiguous(configuration) || complexDirectiveScope(configuration, "add_header")
	if invalid > 0 {
		return checkutil.Value(spec.definition, current, false,
			"At least one directly observed HTTP server/location has an invalid static header value or omits always", now)
	}
	if dynamic > 0 || ambiguous {
		return checkutil.ManualReview(spec.definition, current,
			"Dynamic values, includes, incomplete structure, or conditional add_header scope prevent proving complete coverage",
			"Evaluated Nginx add_header replacement inheritance for directly observed HTTP contexts", now)
	}
	if missing > 0 {
		return checkutil.Value(spec.definition, current, false,
			"At least one directly observed HTTP server/location lacks the required inherited static header value with always", now)
	}
	return checkutil.Value(spec.definition, current, true,
		"Every directly observed HTTP server/location inherits the required static header value with always", now)
}

func matchingHeaders(values []directive, name string) []directive {
	result := make([]directive, 0)
	for _, current := range values {
		if len(current.Args) > 0 && strings.EqualFold(current.Args[0], name) {
			result = append(result, current)
		}
	}
	return result
}

func headerValue(value directive) (string, bool, bool) {
	if len(value.Args) < 2 {
		return "", false, false
	}
	end := len(value.Args)
	always := strings.EqualFold(value.Args[end-1], "always")
	if always {
		end--
	}
	joined := strings.Join(value.Args[1:end], " ")
	return joined, always, strings.Contains(joined, "$")
}

func normalizedHeaderValue(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func complexDirectiveScope(configuration parsedConfig, names ...string) bool {
	targets := make(map[string]struct{}, len(names))
	for _, name := range names {
		targets[name] = struct{}{}
	}
	for _, current := range configuration.Directives {
		if _, relevant := targets[current.Name]; !relevant || !contextStartsWith(current.Context, "http") {
			continue
		}
		for _, frame := range current.Context {
			switch frame.Name {
			case "http", "server", "location":
			default:
				return true
			}
		}
	}
	return false
}
