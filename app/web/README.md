# Setpoint Web 控制台

本目录包含 Setpoint 内部只读安全检查控制台，使用 React、TypeScript、Tailwind CSS 和 Vite 构建。

## 本地开发

在本目录安装依赖并启动开发服务器：

```powershell
pnpm install --frozen-lockfile
pnpm dev
```

开发服务器把 `/api` 和 `/healthz` 代理到本机 `127.0.0.1:8080`。Go Server 仍保持默认回环监听边界。

## 验证与构建

```powershell
pnpm typecheck
pnpm test
pnpm build
```

`pnpm build` 把生产静态资源写入 `app/internal/webui/dist/`。Go Server 通过 `internal/webui` 嵌入并提供这些文件：

- `/api/*` 和 `/healthz` 始终由后端处理，不执行 SPA 回退；
- 带哈希的静态资源使用长期不可变缓存；
- 页面路由回退到 `index.html`；
- 响应附带内容安全策略和基础安全头。
