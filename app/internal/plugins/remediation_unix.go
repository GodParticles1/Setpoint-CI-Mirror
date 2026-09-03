package plugins

import "setpoint/internal/plugin"

const (
	icmpRuntimeControlledReason = "The runtime-only Check does not prove persistent loading state, so durable remediation needs a controlled plan rather than automatic execution."
	icmpPersistedAutoReason     = "AUTO_SAFE only for the exact runtime=1; persisted=0 finding; the existing bounded operation rechecks persistent evidence and restores the exact runtime pre-state."
	sourceRouteControlledReason = "The target is bounded after host-role approval, but the change is connection-affecting and must remain controlled."
	filePermissionReason        = "The path and observed metadata are deterministic, but approved mode/group conventions and high-impact ownership changes require controlled approval."
	shellManualReason           = "Only /etc/profile is observed; the effective system/user Shell source and final value are not proven."
	bannerManualReason          = "The approved warning text is organization/customer policy and is not deterministic from the Check."
	loginDefaultsReason         = "The observed value is a new-account default; changing it is deterministic enough to plan but account/login impact requires controlled approval."
	passwordChainReason         = "A single file does not prove the effective PAM/pwquality authentication chain or the exact safe mutation source."
	accountPermissionReason     = "The fixed account file is deterministic, but it is security-critical and ownership/mode repair requires controlled approval."
	passwordWarnReason          = "PASS_WARN_AGE is a bounded new-account default whose credential-lifecycle impact requires controlled approval."
	auditServiceReason          = "Service state can be changed through a bounded service operation, but audit/logging availability must be explicitly approved and verified."
	auditPermissionReason       = "The fixed audit path is deterministic, but ownership/mode changes can affect rotation and approved readers and must be controlled."
	accountMutationReason       = "The aggregate finding omits identity-specific repair data and requires per-account credential or UID decisions."
	serviceInventoryReason      = "The approved service/listener set is host-role and business specific, so the inventory cannot determine a repair target."
)

var linuxRemediation = map[string]plugin.RemediationMetadata{
	"audit.service.auditd":            remediation(plugin.RemediationControlled, auditServiceReason),
	"audit.service.rsyslog":           remediation(plugin.RemediationControlled, auditServiceReason),
	"audit.log.directory_permissions": remediation(plugin.RemediationControlled, auditPermissionReason),
	"audit.log.file_permissions":      remediation(plugin.RemediationControlled, auditPermissionReason),
	"account.empty_password_hashes":   remediation(plugin.RemediationManualOnly, accountMutationReason),
	"account.duplicate_uids":          remediation(plugin.RemediationManualOnly, accountMutationReason),
	"service.listening_inventory":     remediation(plugin.RemediationManualOnly, serviceInventoryReason),
	"service.enabled_inventory":       remediation(plugin.RemediationManualOnly, serviceInventoryReason),

	"shell.tmout":         remediation(plugin.RemediationManualOnly, shellManualReason),
	"shell.umask":         remediation(plugin.RemediationManualOnly, shellManualReason),
	"shell.histsize":      remediation(plugin.RemediationManualOnly, shellManualReason),
	"login.banner":        remediation(plugin.RemediationManualOnly, bannerManualReason),
	"password.max_days":   remediation(plugin.RemediationControlled, loginDefaultsReason),
	"password.min_days":   remediation(plugin.RemediationControlled, loginDefaultsReason),
	"password.min_length": remediation(plugin.RemediationManualOnly, passwordChainReason),
	"permissions.shadow":  remediation(plugin.RemediationControlled, accountPermissionReason),
	"permissions.passwd":  remediation(plugin.RemediationControlled, accountPermissionReason),
	"login.motd":          remediation(plugin.RemediationManualOnly, bannerManualReason),

	"permissions.group":              remediation(plugin.RemediationControlled, filePermissionReason),
	"permissions.gshadow":            remediation(plugin.RemediationControlled, filePermissionReason),
	"permissions.services":           remediation(plugin.RemediationControlled, filePermissionReason),
	"permissions.login_defs":         remediation(plugin.RemediationControlled, filePermissionReason),
	"permissions.security_directory": remediation(plugin.RemediationControlled, filePermissionReason),
	"permissions.cron_spool":         remediation(plugin.RemediationControlled, filePermissionReason),
	"permissions.wtmp":               remediation(plugin.RemediationControlled, filePermissionReason),

	"net.ipv4.conf.all.accept_redirects":               remediation(plugin.RemediationControlled, icmpRuntimeControlledReason),
	"net.ipv4.conf.default.accept_redirects":           remediation(plugin.RemediationControlled, icmpRuntimeControlledReason),
	"net.ipv4.conf.all.send_redirects":                 remediation(plugin.RemediationControlled, icmpRuntimeControlledReason),
	"net.ipv4.conf.default.send_redirects":             remediation(plugin.RemediationControlled, icmpRuntimeControlledReason),
	"net.ipv4.conf.all.accept_redirects.persisted":     autoSafe(icmpRedirectRuntimeRepairOperationID, icmpPersistedAutoReason),
	"net.ipv4.conf.default.accept_redirects.persisted": autoSafe(icmpRedirectRuntimeRepairOperationID, icmpPersistedAutoReason),
	"net.ipv4.conf.all.send_redirects.persisted":       autoSafe(icmpRedirectRuntimeRepairOperationID, icmpPersistedAutoReason),
	"net.ipv4.conf.default.send_redirects.persisted":   autoSafe(icmpRedirectRuntimeRepairOperationID, icmpPersistedAutoReason),

	"net.ipv4.conf.all.accept_source_route":               remediation(plugin.RemediationControlled, sourceRouteControlledReason),
	"net.ipv4.conf.default.accept_source_route":           remediation(plugin.RemediationControlled, sourceRouteControlledReason),
	"net.ipv4.conf.all.accept_source_route.persisted":     remediation(plugin.RemediationControlled, sourceRouteControlledReason),
	"net.ipv4.conf.default.accept_source_route.persisted": remediation(plugin.RemediationControlled, sourceRouteControlledReason),

	"password.pwquality.min_length":       remediation(plugin.RemediationManualOnly, passwordChainReason),
	"password.pwquality.digit_credit":     remediation(plugin.RemediationManualOnly, passwordChainReason),
	"password.pwquality.uppercase_credit": remediation(plugin.RemediationManualOnly, passwordChainReason),
	"password.pwquality.lowercase_credit": remediation(plugin.RemediationManualOnly, passwordChainReason),
	"password.pwquality.other_credit":     remediation(plugin.RemediationManualOnly, passwordChainReason),
	"password.warn_days":                  remediation(plugin.RemediationControlled, passwordWarnReason),
}
