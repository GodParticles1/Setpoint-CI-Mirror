package plugins

import "setpoint/internal/plugin"

const (
	nginxDeterministicConfigReason = "The finding has a bounded configuration target, but Nginx edits and reload/traffic validation can affect business and require controlled approval."
	nginxLocationReason            = "The parser can identify an unsafe location/alias boundary, but editing the owning configuration and validating routing semantics must be controlled."
	nginxVersionReason             = "A version string does not determine an approved upgrade target or distribution backport state."
	nginxSyntaxReason              = "A syntax failure identifies a blocker but does not determine the correct configuration edit."
	nginxWorkerReason              = "The approved non-root worker identity and filesystem/upstream permissions are business specific."
	nginxCipherReason              = "The Check cannot prove the final OpenSSL-expanded suite or a universally compatible replacement expression."
	nginxErrorPageReason           = "The approved error-page URI and route are business specific and are not frozen by the Check."
	nginxCorsReason                = "Although allowed origins may be policy inputs, the effective dynamic/static configuration source and safe edit are not uniquely determined."
)

var nginxRemediation = map[string]plugin.RemediationMetadata{
	"nginx.version":                       remediation(plugin.RemediationManualOnly, nginxVersionReason),
	"nginx.syntax":                        remediation(plugin.RemediationManualOnly, nginxSyntaxReason),
	"nginx.worker_user":                   remediation(plugin.RemediationManualOnly, nginxWorkerReason),
	"nginx.server_tokens":                 remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.tls_protocols":                 remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.weak_ciphers":                  remediation(plugin.RemediationManualOnly, nginxCipherReason),
	"nginx.hsts":                          remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.location_alias_boundary":       remediation(plugin.RemediationControlled, nginxLocationReason),
	"nginx.header.x_frame_options":        remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.header.x_xss_protection":       remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.header.x_content_type_options": remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.header.referrer_policy":        remediation(plugin.RemediationControlled, nginxDeterministicConfigReason),
	"nginx.error_page_404":                remediation(plugin.RemediationManualOnly, nginxErrorPageReason),
	"nginx.cors.allow_origin":             remediation(plugin.RemediationManualOnly, nginxCorsReason),
}
