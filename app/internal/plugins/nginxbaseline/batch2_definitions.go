package nginxbaseline

import "setpoint/internal/plugins/checkutil"

const corsAllowedOriginsParameter = "cors_allowed_origins"

var batch2Definitions = []checkutil.Definition{
	{
		ID: "nginx.location_alias_boundary", Name: "Nginx location/alias 边界", Recommended: "prefix location 与 alias 使用一致且明确的尾斜杠边界",
		Risk: "high", Description: "location 缺少尾斜杠而 alias 以斜杠结尾可能允许路径越界。",
		Remediation: "核对全部 server/location/include 语义后修正 location 与 alias；本检查不修改或重载 Nginx。", MayAffectBusiness: true,
	},
	{
		ID: "nginx.header.x_frame_options", Name: "X-Frame-Options 覆盖", Recommended: "所有 HTTP server/location 返回 SAMEORIGIN 或 DENY，并带 always",
		Risk: "medium", Description: "缺少或被子上下文覆盖的 X-Frame-Options 会削弱点击劫持防护。",
		Remediation: "确认嵌入需求和 add_header 继承后配置 X-Frame-Options。", MayAffectBusiness: true,
	},
	{
		ID: "nginx.header.x_xss_protection", Name: "X-XSS-Protection 覆盖", Recommended: "所有 HTTP server/location 返回 1; mode=block，并带 always",
		Risk: "low", Description: "来源要求显式声明历史浏览器 XSS 过滤策略；配置声明不证明现代浏览器行为。",
		Remediation: "确认客户端兼容性和 add_header 继承后按来源 Policy 配置。", MayAffectBusiness: true,
	},
	{
		ID: "nginx.header.x_content_type_options", Name: "X-Content-Type-Options 覆盖", Recommended: "所有 HTTP server/location 返回 nosniff，并带 always",
		Risk: "medium", Description: "缺少 nosniff 可能使浏览器对响应类型进行非预期推断。",
		Remediation: "确认响应 Content-Type 后配置 nosniff 并核对继承。", MayAffectBusiness: true,
	},
	{
		ID: "nginx.header.referrer_policy", Name: "Referrer-Policy 覆盖", Recommended: "所有 HTTP server/location 返回 no-referrer-when-downgrade，并带 always",
		Risk: "medium", Description: "缺少明确 Referrer Policy 可能泄露不必要的来源信息。",
		Remediation: "结合业务跳转需求配置来源要求的 Referrer-Policy 并核对继承。", MayAffectBusiness: true,
	},
	{
		ID: "nginx.error_page_404", Name: "Nginx 404 error_page 声明", Recommended: "所有 HTTP server/location 有可继承的 404 error_page 声明",
		Risk: "low", Description: "缺少统一 404 声明可能暴露默认错误页面；本检查不证明目标页面可访问。",
		Remediation: "确认错误页 URI 和业务路由后配置 error_page；另行验证页面可达性。", MayAffectBusiness: true,
	},
	{
		ID: "nginx.cors.allow_origin", Name: "Access-Control-Allow-Origin Policy", Recommended: "无 wildcard，且静态 Origin 与冻结业务 Policy 精确匹配",
		Risk: "high", Description: "过宽或不匹配的 CORS Origin 可能允许非预期站点读取响应。",
		Remediation: "由业务批准允许 Origin 后配置最小静态集合；动态映射需人工复核。", MayAffectBusiness: true,
	},
}
