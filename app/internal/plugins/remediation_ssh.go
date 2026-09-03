package plugins

import "setpoint/internal/plugin"

const (
	sshControlledReason = "The effective directive and bounded target can be planned, but SSH changes can affect access and require controlled approval, syntax validation, and connection-safe verification."
	sshPortReason       = "The approved port and migration sequence are site specific and connection affecting."
	sshBannerReason     = "The Check observes only the default-context path; approved content and Match applicability are not determined."
	sshCipherReason     = "Client compatibility and all Match contexts are not proven by the current Check, so a safe replacement set is not deterministic."
	sshSyntaxReason     = "A syntax failure is a blocker but does not identify the correct configuration edit."
	sshListenerReason   = "Listener findings describe access-path state; deciding which listener/configuration to change requires explicit site access planning."
)

var sshRemediation = map[string]plugin.RemediationMetadata{
	"ssh.permit_empty_passwords":           remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.permit_root_login":                remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.max_auth_tries":                   remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.x11_forwarding":                   remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.client_alive_interval":            remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.client_alive_count_max":           remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.password_authentication":          remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.port":                             remediation(plugin.RemediationManualOnly, sshPortReason),
	"ssh.banner":                           remediation(plugin.RemediationManualOnly, sshBannerReason),
	"ssh.ciphers":                          remediation(plugin.RemediationManualOnly, sshCipherReason),
	"ssh.syntax":                           remediation(plugin.RemediationManualOnly, sshSyntaxReason),
	"ssh.config_permissions":               remediation(plugin.RemediationControlled, sshControlledReason),
	"ssh.listener.configured_ports_active": remediation(plugin.RemediationManualOnly, sshListenerReason),
	"ssh.listener.unexpected_ports":        remediation(plugin.RemediationManualOnly, sshListenerReason),
}
