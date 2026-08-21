package linuxbaseline

func sourceRefs(id string) []string {
	switch id {
	case "shell.tmout":
		return []string{"security-baseline:1.7", "d9d10:module-32"}
	case "shell.umask":
		return []string{"security-baseline:1.6", "d9d10:module-43"}
	case "shell.histsize":
		return []string{"security-baseline:1.8"}
	case "login.banner":
		return []string{"security-baseline:1.11"}
	case "login.motd":
		return []string{"security-baseline:1.11"}
	case "password.max_days", "password.min_days":
		return []string{"security-baseline:1.2", "d9d10:module-31"}
	case "password.min_length":
		return []string{"security-baseline:1.1", "d9d10:module-31"}
	case "permissions.shadow", "permissions.passwd":
		return []string{"security-baseline:1.12", "d9d10:module-45"}
	default:
		return nil
	}
}
