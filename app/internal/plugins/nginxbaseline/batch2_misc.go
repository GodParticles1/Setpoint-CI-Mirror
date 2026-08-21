package nginxbaseline

import (
	"fmt"
	"strings"
	"time"

	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

func batch2Items(configuration parsedConfig, now time.Time, selected map[string]struct{}, policy corsPolicy) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(batch2Definitions))
	if checkSelected(selected, batch2Definitions[0].ID) {
		items = append(items, locationAliasItem(configuration, now))
	}
	for _, spec := range securityHeaderSpecs() {
		if checkSelected(selected, spec.definition.ID) {
			items = append(items, securityHeaderItem(configuration, spec, now))
		}
	}
	if checkSelected(selected, batch2Definitions[5].ID) {
		items = append(items, errorPage404Item(configuration, now))
	}
	if checkSelected(selected, batch2Definitions[6].ID) {
		items = append(items, corsItem(configuration, policy, now))
	}
	return items
}

func errorPage404Item(configuration parsedConfig, now time.Time) task.CheckItem {
	definition := batch2Definitions[5]
	contexts := httpEvaluationContexts(configuration)
	if len(contexts) == 0 {
		if httpConfigAmbiguous(configuration) {
			return checkutil.ManualReview(definition, "no directly scoped HTTP context",
				"Includes or detached server blocks prevent reconstructing 404 declaration inheritance",
				"No target-page reachability test was performed", now)
		}
		return checkutil.NotApplicable(definition, "No HTTP server or location block was directly observed", now)
	}
	covered, missing, dynamic := 0, 0, 0
	for _, context := range contexts {
		values := effectiveDirectives(configuration.Directives, "error_page", context)
		found, targetDynamic := false, false
		for _, value := range values {
			if errorPageHandles(value, "404") {
				found = true
				targetDynamic = targetDynamic || strings.Contains(value.Args[len(value.Args)-1], "$")
			}
		}
		switch {
		case !found:
			missing++
		case targetDynamic:
			dynamic++
		default:
			covered++
		}
	}
	current := fmt.Sprintf("%d context(s): %d declared, %d missing, %d dynamic", len(contexts), covered, missing, dynamic)
	if missing > 0 && !httpConfigAmbiguous(configuration) {
		return checkutil.Value(definition, current, false,
			"At least one directly observed HTTP server/location lacks an inherited 404 error_page declaration", now)
	}
	if dynamic > 0 || httpConfigAmbiguous(configuration) || complexDirectiveScope(configuration, "error_page") {
		return checkutil.ManualReview(definition, current,
			"Dynamic targets, includes, incomplete structure, or conditional scope prevent proving declaration coverage",
			"Checked declaration and inheritance only; target-page reachability was not tested", now)
	}
	return checkutil.Value(definition, current, true,
		"Every directly observed HTTP server/location has an inherited static 404 declaration; page reachability was not claimed", now)
}

func errorPageHandles(value directive, code string) bool {
	if len(value.Args) < 2 {
		return false
	}
	for _, argument := range value.Args[:len(value.Args)-1] {
		if argument == code || strings.HasPrefix(argument, code+"=") {
			return true
		}
	}
	return false
}

func corsItem(configuration parsedConfig, policy corsPolicy, now time.Time) task.CheckItem {
	definition := batch2Definitions[6]
	contexts := httpEvaluationContexts(configuration)
	if len(contexts) == 0 {
		if httpConfigAmbiguous(configuration) {
			return checkutil.ManualReview(definition, "no directly scoped HTTP context",
				"Includes or detached server blocks prevent reconstructing CORS header inheritance",
				"No browser or live-URL test was performed", now)
		}
		return checkutil.NotApplicable(definition, "No HTTP server or location block was directly observed", now)
	}
	missing, wildcard, dynamic, mismatch, matched := 0, 0, 0, 0, 0
	observed := map[string]struct{}{}
	for _, context := range contexts {
		headers := matchingHeaders(effectiveDirectives(configuration.Directives, "add_header", context), "Access-Control-Allow-Origin")
		if len(headers) == 0 {
			missing++
			continue
		}
		for _, header := range headers {
			value, _, isDynamic := headerValue(header)
			value = strings.TrimSpace(value)
			if value == "*" {
				wildcard++
				continue
			}
			if isDynamic {
				dynamic++
				continue
			}
			origin, err := normalizeOrigin(value)
			if err != nil {
				mismatch++
				continue
			}
			observed[origin] = struct{}{}
			if _, allowed := policy.Allowed[origin]; policy.Configured && allowed {
				matched++
			} else if policy.Configured {
				mismatch++
			}
		}
	}
	current := fmt.Sprintf("%d context(s): %d matched, %d missing, %d wildcard, %d dynamic, %d mismatch",
		len(contexts), matched, missing, wildcard, dynamic, mismatch)
	if wildcard > 0 || mismatch > 0 || policy.Configured && missing > 0 && !httpConfigAmbiguous(configuration) {
		return checkutil.Value(definition, current, false,
			"A wildcard, non-origin value, unapproved origin, or missing configured declaration was directly observed", now)
	}
	if !policy.Configured {
		return checkutil.ManualReview(definition, current,
			"No approved business Origin policy was supplied, so static declarations cannot be judged compliant",
			"Observed CORS declarations without exposing raw Origin values", now)
	}
	missingApproved := 0
	for allowed := range policy.Allowed {
		if _, exists := observed[allowed]; !exists {
			missingApproved++
		}
	}
	if dynamic > 0 || missingApproved > 0 || httpConfigAmbiguous(configuration) ||
		complexDirectiveScope(configuration, "add_header") {
		return checkutil.ManualReview(definition, current,
			"Dynamic or ambiguous inheritance remains, or not every approved Origin was directly observed",
			"Compared static declarations with the frozen policy without browser or live-URL testing", now)
	}
	return checkutil.Value(definition, current, true,
		"All effective static Origin declarations exactly matched the frozen policy; browser behavior was not tested", now)
}
