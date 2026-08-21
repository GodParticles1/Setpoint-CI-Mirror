package sshbaseline

func sourceRefs(id string) []string {
	references := []string{"security-baseline:1.16", "d9d10:module-42"}
	switch id {
	case "ssh.listener.configured_ports_active", "ssh.listener.unexpected_ports":
		return []string{"security-baseline:1.16"}
	case "ssh.x11_forwarding":
		return append(references, "security-baseline:1.18")
	case "ssh.permit_root_login":
		return append(references, "d9d10:module-44")
	case "ssh.config_permissions":
		return []string{"security-baseline:1.12", "security-baseline:1.16", "d9d10:module-45"}
	default:
		return references
	}
}
