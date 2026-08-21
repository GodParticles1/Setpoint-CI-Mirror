package nginxbaseline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

const ID = "nginx.baseline.core"

var definitions = append([]checkutil.Definition{
	{ID: "nginx.version", Name: "Nginx 版本观察", Recommended: "按组织支持矩阵和补丁来源人工确认", Risk: "medium", Description: "版本字符串只能用于资产识别，不能单独证明漏洞修复或支持状态。", Remediation: "结合组织支持矩阵、发行版回移补丁和漏洞公告评估升级；本插件不执行升级。", MayAffectBusiness: true},
	{ID: "nginx.syntax", Name: "Nginx 配置语法", Recommended: "nginx -t 退出码 0", Risk: "high", Description: "语法错误会阻止安全重载并可能影响服务恢复。", Remediation: "修正配置后再次执行 nginx -t；本插件不重载服务。", MayAffectBusiness: true},
	{ID: "nginx.worker_user", Name: "Nginx Worker 运行用户", Recommended: "显式非 root 用户", Risk: "high", Description: "Worker 使用高权限账号会扩大漏洞影响。", Remediation: "确认目录和上游权限后配置专用非 root 用户。", MayAffectBusiness: true},
	{ID: "nginx.server_tokens", Name: "Nginx 版本信息暴露", Recommended: "所有适用上下文继承 server_tokens off", Risk: "low", Description: "响应中暴露版本会帮助攻击者定位已知漏洞。", Remediation: "在可覆盖全部站点的 http 上下文配置 server_tokens off，并检查覆盖。", MayAffectBusiness: true},
	{ID: "nginx.tls_protocols", Name: "Nginx TLS 协议覆盖", Recommended: "所有 TLS server 仅启用 TLSv1.2/TLSv1.3", Risk: "high", Description: "旧 TLS/SSL 协议存在已知弱点；局部安全配置不能证明所有 TLS server 都安全。", Remediation: "完成客户端兼容性评估后在可证明覆盖的上下文收紧 ssl_protocols。", MayAffectBusiness: true},
	{ID: "nginx.weak_ciphers", Name: "Nginx cipher 表达式复核", Recommended: "展开后的套件不启用 3DES、RC4、NULL、EXPORT、MD5", Risk: "high", Description: "仅检查表达式字符串不能可靠证明 OpenSSL 最终展开套件安全。", Remediation: "结合实际 OpenSSL 版本展开表达式并验证真实握手后调整 ssl_ciphers。", MayAffectBusiness: true},
	{ID: "nginx.hsts", Name: "Nginx HSTS 覆盖", Recommended: "所有 TLS server 返回 max-age>=31536000 且带 always 的 Strict-Transport-Security", Risk: "medium", Description: "HSTS 的值、always、TLS 适用范围和 add_header 继承都会影响真实覆盖。", Remediation: "确认所有 HTTPS 站点、子域和 add_header 继承后再配置 HSTS。", MayAffectBusiness: true},
}, batch2Definitions...)

type Plugin struct{}

func New() Plugin { return Plugin{} }

func (Plugin) Metadata() plugin.Metadata {
	checks := make([]plugin.CheckItemDefinition, 0, len(definitions))
	for _, definition := range definitions {
		checks = append(checks, plugin.CheckItemDefinition{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RecommendedValue: definition.Recommended, SourceRefs: sourceRefs(definition.ID),
		})
	}
	return plugin.Metadata{
		ID: ID, Category: "Nginx", Name: "Nginx 基础安全检查", Version: "2.2.0",
		Description: "只读观察 Nginx 版本、语法、运行用户、HTTP 安全声明和 TLS 配置；复杂继承或表达式无法闭合时要求人工复核。",
		Mode:        plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "只执行 nginx -v、nginx -t 和有输出上限的 nginx -T",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{{
			Name: corsAllowedOriginsParameter, Type: "string",
			Description: "批准的静态 HTTP/HTTPS Origin，逗号分隔；省略时 CORS 结论保持人工复核",
		}}, Checks: checks,
	}
}

func (Plugin) Detect(ctx context.Context, input plugin.CheckInput) (plugin.Detection, error) {
	result, err := input.Executor.Execute(ctx, executor.Command{Name: "nginx", Args: []string{"-v"}})
	if errors.Is(err, executor.ErrCommandNotFound) {
		return plugin.Detection{Applicable: false, Reason: "nginx is not installed in trusted system directories"}, nil
	}
	if err != nil {
		return plugin.Detection{Applicable: true}, fmt.Errorf("read nginx version: %w", err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return plugin.Detection{Applicable: true}, errors.New("nginx -v output exceeded the configured limit")
	}
	return plugin.Detection{Applicable: true}, nil
}

func (Plugin) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	now := time.Now().UTC()
	selected := selectedCheckSet(input.SelectedCheckIDs)
	items := make([]task.CheckItem, 0, len(definitions))
	var failures []error
	var currentCORSPolicy corsPolicy
	if checkSelected(selected, batch2Definitions[6].ID) {
		var policyErr error
		currentCORSPolicy, policyErr = parseCORSPolicy(input.Parameters)
		if policyErr != nil {
			items = append(items, checkutil.Error(batch2Definitions[6], "invalid_check_parameters", policyErr.Error(),
				"Nginx CORS policy parameters were rejected before observation", now))
			return items, policyErr
		}
	}

	if checkSelected(selected, definitions[0].ID) {
		versionResult, versionErr := input.Executor.Execute(ctx, executor.Command{Name: "nginx", Args: []string{"-v"}})
		version := boundedSummary(strings.TrimSpace(versionResult.Stdout + " " + versionResult.Stderr))
		if versionErr != nil {
			items = append(items, checkutil.Error(definitions[0], checkutil.ErrorCode(versionErr, "nginx_version_failed"), versionErr.Error(), "Unable to execute nginx -v", now))
			failures = append(failures, versionErr)
		} else if version == "" {
			emptyErr := errors.New("nginx -v returned no version")
			items = append(items, checkutil.Error(definitions[0], "nginx_version_missing", emptyErr.Error(), "Version output was empty", now))
			failures = append(failures, emptyErr)
		} else {
			items = append(items, checkutil.ManualReview(definitions[0], version,
				"A version string alone cannot prove organization support, distribution backports, or vulnerability remediation",
				"Read bounded nginx -v output; no support-matrix or vulnerability lookup was performed", now))
		}
	}

	if checkSelected(selected, definitions[1].ID) {
		syntaxResult, syntaxErr := input.Executor.Execute(ctx, executor.Command{Name: "nginx", Args: []string{"-t"}})
		if syntaxErr != nil {
			items = append(items, checkutil.Error(definitions[1], checkutil.ErrorCode(syntaxErr, "nginx_syntax_failed"), syntaxErr.Error(), boundedSummary(syntaxResult.Stderr), now))
			failures = append(failures, syntaxErr)
		} else {
			items = append(items, checkutil.Value(definitions[1], "valid", true, "nginx -t completed successfully without reloading the service", now))
		}
	}

	if anyCheckSelected(selected, definitions[2:]...) {
		configResult, configErr := input.Executor.Execute(ctx, executor.Command{Name: "nginx", Args: []string{"-T"}})
		if configErr == nil && (configResult.StdoutTruncated || configResult.StderrTruncated) {
			configErr = errors.New("nginx -T output exceeded the configured limit")
		}
		if configErr != nil {
			for _, definition := range definitions[2:] {
				if checkSelected(selected, definition.ID) {
					items = append(items, checkutil.Error(definition, checkutil.ErrorCode(configErr, "nginx_config_read_failed"), configErr.Error(), "Unable to obtain complete nginx -T output", now))
				}
			}
			failures = append(failures, configErr)
			return items, errors.Join(failures...)
		}
		configuration := parseConfig(configResult.Stdout)
		items = append(items, configItems(configuration, now, selected, currentCORSPolicy)...)
	}
	return items, errors.Join(failures...)
}

func configItems(configuration parsedConfig, now time.Time, selected map[string]struct{}, currentCORSPolicy corsPolicy) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(definitions)-2)
	directives := configuration.Directives
	if checkSelected(selected, definitions[2].ID) {
		items = append(items, workerUserItem(directives, now))
	}
	httpValues := httpDirectives(directives)
	if len(httpValues) == 0 && !hasHTTPBlock(configuration) {
		for _, definition := range definitions[3:] {
			if checkSelected(selected, definition.ID) {
				items = append(items, checkutil.NotApplicable(definition,
					"No HTTP-context directive was detected; stream configuration is outside these HTTP checks", now))
			}
		}
		return items
	}
	if checkSelected(selected, definitions[3].ID) {
		items = append(items, serverTokensItem(httpValues, now))
	}

	if anyCheckSelected(selected, definitions[4:7]...) && !tlsConfigured(httpValues) {
		for _, definition := range definitions[4:7] {
			if checkSelected(selected, definition.ID) {
				items = append(items, checkutil.NotApplicable(definition,
					"No HTTP TLS listener was detected in complete nginx -T output", now))
			}
		}
	} else if checkSelected(selected, definitions[4].ID) {
		items = append(items, tlsProtocolsItem(httpValues, now))
		if checkSelected(selected, definitions[5].ID) {
			items = append(items, cipherItem(httpValues, now))
		}
		if checkSelected(selected, definitions[6].ID) {
			items = append(items, hstsItem(httpValues, now))
		}
	} else {
		if checkSelected(selected, definitions[5].ID) {
			items = append(items, cipherItem(httpValues, now))
		}
		if checkSelected(selected, definitions[6].ID) {
			items = append(items, hstsItem(httpValues, now))
		}
	}
	items = append(items, batch2Items(configuration, now, selected, currentCORSPolicy)...)
	return items
}

func hasHTTPBlock(configuration parsedConfig) bool {
	for _, current := range configuration.Blocks {
		if current.Frame.Name == "http" && len(current.Parents) == 0 {
			return true
		}
	}
	return false
}

func selectedCheckSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	return selected
}

func checkSelected(selected map[string]struct{}, id string) bool {
	if len(selected) == 0 {
		return true
	}
	_, exists := selected[id]
	return exists
}

func anyCheckSelected(selected map[string]struct{}, definitions ...checkutil.Definition) bool {
	for _, definition := range definitions {
		if checkSelected(selected, definition.ID) {
			return true
		}
	}
	return false
}

func httpDirectives(directives []directive) []directive {
	result := make([]directive, 0, len(directives))
	for _, current := range directives {
		if contextStartsWith(current.Context, "http") {
			result = append(result, current)
		}
	}
	return result
}

func workerUserItem(directives []directive, now time.Time) task.CheckItem {
	users := findDirectives(directives, "user")
	value := valueOrMissing(joinedArgs(users))
	if len(users) == 0 {
		return checkutil.ManualReview(definitions[2], value,
			"No explicit user directive was found; the build-time default and actual worker identity were not observed",
			"Parsed complete nginx -T output without finding a user directive", now)
	}
	for _, current := range users {
		if len(current.Args) == 0 || strings.EqualFold(current.Args[0], "root") {
			return checkutil.Value(definitions[2], value, false, "Parsed user directives from complete nginx -T output", now)
		}
	}
	if len(users) > 1 {
		return checkutil.ManualReview(definitions[2], value,
			"Multiple user directives were reported and the parser does not determine which declaration nginx accepted",
			"Parsed multiple user directives from complete nginx -T output", now)
	}
	return checkutil.Value(definitions[2], value, true, "Parsed the main-context user directive from complete nginx -T output", now)
}

func serverTokensItem(directives []directive, now time.Time) task.CheckItem {
	values := findDirectives(directives, "server_tokens")
	current := valueOrMissing(joinedArgs(values))
	if len(values) == 0 {
		return checkutil.Value(definitions[3], current, false, "No server_tokens directive was present; nginx defaults to version disclosure", now)
	}
	allOff, hasHTTP := true, false
	for _, value := range values {
		allOff = allOff && len(value.Args) > 0 && strings.EqualFold(value.Args[0], "off")
		hasHTTP = hasHTTP || atHTTPContext(value)
	}
	if !allOff {
		return checkutil.Value(definitions[3], current, false, "At least one server_tokens directive was not off", now)
	}
	if !hasHTTP {
		return checkutil.ManualReview(definitions[3], current,
			"Only nested server_tokens directives were found; coverage of every server block cannot be proven",
			"Parsed all server_tokens directives and their context types from nginx -T", now)
	}
	return checkutil.Value(definitions[3], current, true, "An http-level server_tokens off covers descendants and no conflicting directive was found", now)
}

func tlsProtocolsItem(directives []directive, now time.Time) task.CheckItem {
	values := findDirectives(directives, "ssl_protocols")
	current := valueOrMissing(joinedArgs(values))
	if len(values) == 0 {
		return checkutil.ManualReview(definitions[4], current,
			"TLS is configured but no explicit ssl_protocols directive was found; build defaults were not resolved",
			"Complete nginx -T output contained TLS configuration without ssl_protocols", now)
	}
	hasHTTP := false
	for _, value := range values {
		if !protocolDirectiveSafe(value) {
			return checkutil.Value(definitions[4], current, false, "At least one ssl_protocols directive enables a protocol outside TLSv1.2/TLSv1.3", now)
		}
		hasHTTP = hasHTTP || atHTTPContext(value)
	}
	if !hasHTTP {
		return checkutil.ManualReview(definitions[4], current,
			"Only nested ssl_protocols directives were found; coverage of every TLS server cannot be proven",
			"All observed ssl_protocols values were modern, but the parser cannot bind them to every TLS server block", now)
	}
	return checkutil.Value(definitions[4], current, true, "An http-level modern ssl_protocols directive covers descendants and no insecure override was found", now)
}

func cipherItem(directives []directive, now time.Time) task.CheckItem {
	values := findDirectives(directives, "ssl_ciphers")
	current := valueOrMissing(joinedArgs(values))
	if knownWeakCipherEnabled(values) {
		return checkutil.Value(definitions[5], current, false, "A known weak cipher token was explicitly enabled in ssl_ciphers", now)
	}
	reason := "The configured OpenSSL cipher expression was not expanded against the actual library, so the effective suite cannot be declared safe"
	if len(values) == 0 {
		reason = "No explicit ssl_ciphers directive was found and the effective build default was not resolved"
	}
	return checkutil.ManualReview(definitions[5], current, reason,
		"Parsed ssl_ciphers expressions from complete nginx -T output without claiming OpenSSL expansion or handshake coverage", now)
}

func hstsItem(directives []directive, now time.Time) task.CheckItem {
	allHeaders := findDirectives(directives, "add_header")
	hsts := make([]directive, 0)
	for _, header := range allHeaders {
		if len(header.Args) > 0 && strings.EqualFold(header.Args[0], "Strict-Transport-Security") {
			hsts = append(hsts, header)
		}
	}
	if len(hsts) == 0 {
		return checkutil.Value(definitions[6], "not configured", false,
			"No Strict-Transport-Security add_header was present in complete nginx -T output", now)
	}
	for _, header := range hsts {
		if !validHSTS(header) {
			return checkutil.Value(definitions[6], "configured with invalid value or scope flags", false,
				"At least one HSTS declaration lacked max-age>=31536000 or the always parameter", now)
		}
	}
	hasHTTPHSTS := false
	hasNestedHeader := false
	for _, header := range allHeaders {
		hasNestedHeader = hasNestedHeader || nestedHTTPContext(header)
	}
	for _, header := range hsts {
		hasHTTPHSTS = hasHTTPHSTS || atHTTPContext(header)
	}
	if !hasHTTPHSTS || hasNestedHeader {
		return checkutil.ManualReview(definitions[6], fmt.Sprintf("%d valid HSTS declaration(s)", len(hsts)),
			"The parser cannot prove HSTS coverage for every TLS server because declarations are nested or child add_header directives may replace inheritance",
			"Validated HSTS header name, max-age and always; server-block identity and full inheritance coverage remain unresolved", now)
	}
	return checkutil.Value(definitions[6], "http-level max-age>=31536000 with always", true,
		"A valid http-level HSTS declaration was found and no nested add_header directive can replace inheritance", now)
}

func tlsConfigured(directives []directive) bool {
	for _, listen := range findDirectives(directives, "listen") {
		for _, argument := range listen.Args {
			if strings.EqualFold(argument, "ssl") {
				return true
			}
		}
	}
	return false
}

func protocolDirectiveSafe(value directive) bool {
	if len(value.Args) == 0 {
		return false
	}
	for _, current := range value.Args {
		switch strings.ToLower(current) {
		case "tlsv1.2", "tlsv1.3":
		default:
			return false
		}
	}
	return true
}

func knownWeakCipherEnabled(values []directive) bool {
	for _, value := range values {
		for _, argument := range value.Args {
			for _, cipher := range strings.FieldsFunc(strings.ToLower(argument), func(character rune) bool {
				return character == ':' || character == ',' || character == ' '
			}) {
				excluded := strings.HasPrefix(cipher, "!") || strings.HasPrefix(cipher, "-")
				name := strings.TrimLeft(cipher, "!-")
				for _, weak := range []string{"3des", "des-cbc3", "rc4", "arcfour", "null", "export", "md5"} {
					if !excluded && strings.Contains(name, weak) {
						return true
					}
				}
			}
		}
	}
	return false
}

var hstsMaxAgePattern = regexp.MustCompile(`(?i)(?:^|;)\s*max-age\s*=\s*([0-9]+)(?:\s*;|$)`)

func validHSTS(value directive) bool {
	if len(value.Args) < 3 || !strings.EqualFold(value.Args[len(value.Args)-1], "always") {
		return false
	}
	headerValue := strings.Join(value.Args[1:len(value.Args)-1], " ")
	match := hstsMaxAgePattern.FindStringSubmatch(headerValue)
	if len(match) != 2 {
		return false
	}
	seconds, err := strconv.ParseUint(match[1], 10, 64)
	return err == nil && seconds >= 31_536_000
}

func atHTTPContext(value directive) bool {
	return len(value.Context) == 1 && value.Context[0].Name == "http"
}

func nestedHTTPContext(value directive) bool {
	return len(value.Context) > 1 && value.Context[0].Name == "http"
}

func valueOrMissing(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not explicitly configured"
	}
	return value
}

func boundedSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240] + "..."
	}
	return value
}
