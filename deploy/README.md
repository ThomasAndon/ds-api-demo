# 生产部署

本项目按宝塔 Go 项目部署：

- 项目名：`ds_api_demo`
- 运行端口：`18082`
- 启动文件：构建后的 `ds-api-demo`
- 工作目录：项目目录
- 环境变量：参考根目录 `.env.example`
- Nginx：`deploy/ds-api-demo.nginx.conf`

构建顺序：先执行 `pnpm install --frozen-lockfile && pnpm build`，再执行
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ds-api-demo .`。

程序首次启动会在 MySQL `redir` 库自动建立 `llm_config` 和
`chat_sessions` 两张表。`CONFIG_SECRET` 用于 AES-GCM 加密 API Key，部署后不要更换，
否则已有 API Key 无法解密。
