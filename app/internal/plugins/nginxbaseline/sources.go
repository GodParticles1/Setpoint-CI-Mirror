package nginxbaseline

func sourceRefs(id string) []string {
	switch id {
	case "nginx.worker_user":
		return []string{"security-baseline:2.10"}
	case "nginx.server_tokens", "nginx.hsts":
		return []string{"security-baseline:2.3"}
	case "nginx.tls_protocols":
		return []string{"security-baseline:2.7"}
	case "nginx.weak_ciphers":
		return []string{"security-baseline:2.4"}
	case "nginx.location_alias_boundary":
		return []string{"security-baseline:2.2"}
	case "nginx.header.x_frame_options", "nginx.header.x_xss_protection",
		"nginx.header.x_content_type_options", "nginx.header.referrer_policy":
		return []string{"security-baseline:2.3"}
	case "nginx.error_page_404":
		return []string{"security-baseline:2.5"}
	case "nginx.cors.allow_origin":
		return []string{"security-baseline:2.6"}
	default:
		return []string{"security-baseline:2"}
	}
}
