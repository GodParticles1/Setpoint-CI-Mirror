package plugins

import (
	"fmt"

	"setpoint/internal/plugin"
)

const icmpRedirectRuntimeRepairOperationID = "linux.network.icmp_redirects.runtime_repair"

func remediation(disposition plugin.RemediationDisposition, reason string) plugin.RemediationMetadata {
	return plugin.RemediationMetadata{Disposition: disposition, Reason: reason}
}

func autoSafe(operationID, reason string) plugin.RemediationMetadata {
	return plugin.RemediationMetadata{Disposition: plugin.RemediationAutoSafe, OperationID: operationID, Reason: reason}
}

func registerFormalRemediation(registry *plugin.CheckRegistry) error {
	catalogs := []map[string]plugin.RemediationMetadata{
		linuxRemediation,
		nginxRemediation,
		sshRemediation,
		clickhouseRemediation,
	}
	seen := make(map[string]struct{}, 84)
	for _, catalog := range catalogs {
		for id, metadata := range catalog {
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("duplicate remediation disposition for check %s", id)
			}
			if _, exists := registry.GetDefinition(id); !exists {
				return fmt.Errorf("remediation disposition references unknown formal check %s", id)
			}
			if err := registry.SetRemediationMetadata(id, metadata); err != nil {
				return err
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != len(registry.ListDefinitions()) {
		return fmt.Errorf("formal remediation catalog covers %d checks, formal catalog has %d", len(seen), len(registry.ListDefinitions()))
	}
	return nil
}
