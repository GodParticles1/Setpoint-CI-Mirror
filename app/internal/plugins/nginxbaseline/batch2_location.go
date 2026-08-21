package nginxbaseline

import (
	"fmt"
	"strings"
	"time"

	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

func locationAliasItem(configuration parsedConfig, now time.Time) task.CheckItem {
	definition := batch2Definitions[0]
	aliases := findDirectives(httpDirectives(configuration.Directives), "alias")
	if len(aliases) == 0 {
		if httpConfigAmbiguous(configuration) {
			return checkutil.ManualReview(definition, "no directly observed alias",
				"Includes or incomplete block structure prevent proving that no HTTP alias is effective",
				"Inspected parsed HTTP directives without expanding ambiguous scope", now)
		}
		return checkutil.NotApplicable(definition, "No alias directive was present in a complete HTTP configuration", now)
	}
	safe, manual, unsafe := 0, 0, 0
	for _, current := range aliases {
		switch classifyLocationAlias(current) {
		case task.ItemSafe:
			safe++
		case task.ItemUnsafe:
			unsafe++
		default:
			manual++
		}
	}
	current := fmt.Sprintf("%d alias declaration(s): %d safe, %d unsafe, %d manual", len(aliases), safe, unsafe, manual)
	if unsafe > 0 {
		return checkutil.Value(definition, current, false,
			"At least one prefix location without a trailing slash maps to an alias ending with a slash", now)
	}
	if manual > 0 || httpConfigAmbiguous(configuration) {
		return checkutil.ManualReview(definition, current,
			"Regex, dynamic, unusual boundary, include, or incomplete scope requires route-aware review",
			"Classified only directly observed static location/alias boundary pairs", now)
	}
	return checkutil.Value(definition, current, true,
		"Every directly observed alias used a provable exact or matching trailing-slash boundary", now)
}

func classifyLocationAlias(value directive) task.ItemStatus {
	if len(value.Args) != 1 || strings.Contains(value.Args[0], "$") || !strings.HasPrefix(value.Args[0], "/") {
		return task.ItemManualReview
	}
	location, exists := innermostContext(value.Context, "location")
	if !exists || len(location.Args) == 0 {
		return task.ItemManualReview
	}
	modifier, path := "", location.Args[0]
	if path == "=" || path == "^~" || path == "~" || path == "~*" {
		modifier = path
		if len(location.Args) < 2 {
			return task.ItemManualReview
		}
		path = location.Args[1]
	}
	if strings.Contains(path, "$") || modifier == "~" || modifier == "~*" {
		return task.ItemManualReview
	}
	if modifier == "=" {
		return task.ItemSafe
	}
	locationSlash := strings.HasSuffix(path, "/")
	aliasSlash := strings.HasSuffix(value.Args[0], "/")
	if !locationSlash && aliasSlash {
		return task.ItemUnsafe
	}
	if locationSlash && aliasSlash {
		return task.ItemSafe
	}
	return task.ItemManualReview
}
