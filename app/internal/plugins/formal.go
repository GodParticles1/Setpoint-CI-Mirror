package plugins

import (
	"fmt"

	"setpoint/internal/plugin"
	"setpoint/internal/plugins/clickhousechecks"
	"setpoint/internal/plugins/linuxaudit"
	"setpoint/internal/plugins/linuxbaseline"
	"setpoint/internal/plugins/linuxfiles"
	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/plugins/linuxnetwork"
	"setpoint/internal/plugins/linuxpassword"
	"setpoint/internal/plugins/nginxbaseline"
	"setpoint/internal/plugins/sshbaseline"
)

func Formal() []plugin.CheckDescriptor {
	return []plugin.CheckDescriptor{
		clickhousechecks.New(),
		linuxaudit.New(),
		linuxbaseline.New(),
		linuxfiles.New(),
		linuxicmpredirects.New(),
		linuxnetwork.New(),
		linuxpassword.New(),
		nginxbaseline.New(),
		sshbaseline.New(),
	}
}

func RegisterFormal(registry *plugin.CheckRegistry) error {
	if registry == nil {
		return fmt.Errorf("check registry is required")
	}
	for _, definition := range Formal() {
		metadata := definition.Metadata()
		if err := registry.Register(definition); err != nil {
			return fmt.Errorf("register formal check %s: %w", metadata.ID, err)
		}
	}
	policies := []plugin.CheckPolicy{
		{ID: "policy.clickhouse-migration-readiness", Name: "ClickHouse 迁移就绪只读观察", Description: "ClickHouse 组件、运行、目录、容量、Replica、拓扑、Atomic EXCHANGE 与 pair 兼容性只读观察集合", BundleIDs: []string{clickhousechecks.ID}},
		{ID: "policy.linux-host-readonly", Name: "Linux 主机只读基线", Description: "Linux 核心、网络、审计、密码声明和固定路径权限观察集合", BundleIDs: []string{linuxaudit.ID, linuxbaseline.ID, linuxfiles.ID, linuxicmpredirects.ID, linuxnetwork.ID, linuxpassword.ID}},
		{ID: "policy.ssh-readonly", Name: "SSH 只读基线", Description: "SSH 有效配置、语法和权限观察集合", BundleIDs: []string{sshbaseline.ID}},
		{ID: "policy.nginx-http-readonly", Name: "Nginx HTTP 只读基线", Description: "Nginx HTTP 配置观察集合，不含证书能力", BundleIDs: []string{nginxbaseline.ID}},
		{ID: "policy.d9d10-host-observation", Name: "D9/D10 主机观察", Description: "从稳定专项工具提炼的账号、审计、日志和服务面只读观察", CheckIDs: []string{
			"account.duplicate_uids", "account.empty_password_hashes", "audit.log.directory_permissions",
			"audit.log.file_permissions", "audit.service.auditd", "audit.service.rsyslog",
			"service.enabled_inventory", "service.listening_inventory",
		}},
		{ID: "policy.security-baseline-m2-batch1", Name: "安全基线 M2 第一批", Description: "密码声明、固定路径权限和 IPv4 源路由的第一批正式只读能力", CheckIDs: []string{
			"permissions.cron_spool", "permissions.group", "permissions.gshadow", "permissions.login_defs",
			"permissions.security_directory", "permissions.services", "permissions.wtmp",
			"net.ipv4.conf.all.accept_source_route", "net.ipv4.conf.default.accept_source_route",
			"password.pwquality.digit_credit", "password.pwquality.lowercase_credit",
			"password.pwquality.min_length", "password.pwquality.other_credit",
			"password.pwquality.uppercase_credit", "password.warn_days",
		}},
		{ID: "policy.security-baseline-m2-batch2", Name: "安全基线 M2 第二批", Description: "Nginx 静态 HTTP 配置和 Linux MOTD 的第二批正式只读能力", CheckIDs: []string{
			"login.motd", "nginx.location_alias_boundary", "nginx.header.x_frame_options",
			"nginx.header.x_xss_protection", "nginx.header.x_content_type_options",
			"nginx.header.referrer_policy", "nginx.error_page_404", "nginx.cors.allow_origin",
		}},
		{ID: "policy.security-baseline-m2-batch3", Name: "安全基线 M2 第三批", Description: "IPv4 源路由与重定向持久化状态及 SSH 监听端口的第三批正式只读能力", CheckIDs: []string{
			"net.ipv4.conf.all.accept_source_route.persisted",
			"net.ipv4.conf.default.accept_source_route.persisted",
			"net.ipv4.conf.all.accept_redirects.persisted",
			"net.ipv4.conf.default.accept_redirects.persisted",
			"net.ipv4.conf.all.send_redirects.persisted",
			"net.ipv4.conf.default.send_redirects.persisted",
			"ssh.listener.configured_ports_active",
			"ssh.listener.unexpected_ports",
		}},
	}
	for _, policy := range policies {
		if err := registry.RegisterPolicy(policy); err != nil {
			return fmt.Errorf("register formal policy %s: %w", policy.ID, err)
		}
	}
	return nil
}
