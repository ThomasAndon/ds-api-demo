# ds-api-demo

这是基于 DeepSeek Chat Demo 改造的私有前后端版本。React 前端只请求同源 Go 后端；
OpenAI 兼容 URL、API Key、模型和温度由 `/admin` 配置，API Key 不会下发到浏览器。
会话、重命名和删除均持久化到 MySQL。

后台支持保存多套 LLM 凭据，每套独立包含名称、URL、加密 API Key、模型和温度。
列表中只有一套处于当前启用状态，可一键切换；前台密码是所有凭据共用的全局设置。

后台默认 API 根地址为 `https://la-api.vxvxvx.top/v1`，可通过上游 `/models`
获取模型列表。前台保持官方项目的界面和 API Key 文案不变；输入值仅用于本站访问控制，
不会作为上游 API Key 发送。

核心接口：

- `POST /api/chat/completions`：浏览器到 Go 的流式 LLM 接口；Go 再请求后台配置的 OpenAI 兼容服务。
- `/api/sessions`：会话列表、详情、保存、重命名、删除和批量导入。
- `/api/admin/profiles`：仅后台登录后可用的多凭据新增、编辑、删除和切换接口。
- `GET /healthz`：应用和数据库健康检查。

官方前端请求中的 `system`、`latest_reminder`、`thinking`、`safety_check`、`tools`
和 `title` 只用于兼容接收，不会透传给上游。上游请求只包含后台选择的模型、温度、
普通 `user/assistant` 消息以及流式参数，本项目不发送 system 消息。

例外是后台 URL 使用 `api.deepseek.com` 时：后端自动切换到 DeepSeek 官方
`/anthropic/v1/messages` 协议，保留 `thinking`、`deepseek_web_search`、
`deepseek_read_url` 和 `latest_reminder`，使官方前端的深入思考与智能搜索按钮生效；
`system` 仍会被删除。其他 OpenAI 兼容中转站继续使用标准 `/chat/completions`，
不会传递这些 DeepSeek 专用字段。

生产部署见 [`deploy/README.md`](deploy/README.md)。

---

[English](README.md) | 简体中文

基于 DeepSeek API 的静态聊天示例。无需应用后端，浏览器直接请求配置的 API 服务。

点击 [在线体验](https://deepseek-ai.github.io/chat-demo/) 即可使用，也可以前往 [Releases](https://github.com/deepseek-ai/deepseek-chat-demo/releases) 下载压缩包，解压后双击 `index.html` 在本地使用，无需安装或构建。首次使用时，请在设置中填写 API Key。

## 运行

准备 Node.js 22.12+ 和 pnpm：

```sh
git clone https://github.com/deepseek-ai/chat-demo.git
cd chat-demo
pnpm install
pnpm dev
```

打开 http://localhost:8090 。

生成静态产物：

```sh
pnpm build
```

产物位于 `dist/`，可部署到静态服务器，也可直接打开 `dist/index.html`。直接打开时，浏览器和 API 服务需允许 `file://` 页面的存储与跨域请求。

## 功能

- 多会话、自动标题、重命名与删除；编辑提问、重新生成和切换消息分支。
- 流式回答、深度思考、智能搜索、链接阅读与来源引用；支持停止和符合条件的继续生成。
- 附件上传、粘贴与拖拽，语音输入和回复朗读；移动端录音时输入框首行显示实时频谱。
- Markdown、代码高亮、KaTeX 公式和 Mermaid 图表。
- 简体中文与英文、浅色与深色主题、移动端布局、位置设置、历史导入导出。

## 数据

API Key 和设置保存在浏览器 localStorage，会话及附件原件保存在 IndexedDB，本地数据按浏览器用户配置和站点源（origin）隔离。发送对话、上传附件和使用语音功能时，相应内容会提交给配置的 API 服务。

设置中的历史导入导出使用 ZIP 格式。导入会在确认并校验成功后替换全部本地会话和附件；导出包含会话与本地可用的图片原件，不包含 API Key、设置或非图片附件原件。格式与保留范围见[历史导入导出](src/services/chat/history/README.md)。

设置 → 数据管理 → 删除所有对话，会在确认后清空全部本地会话、附件原件和未发送的草稿，停止正在生成的回复并回到新对话。API Key 和其他设置保留，已上传到 API 服务的文件不受影响。

## 开发

| 命令          | 用途                |
| ------------- | ------------------- |
| `pnpm dev`    | 启动开发服务器      |
| `pnpm build`  | 构建到 `dist/`      |
| `pnpm type`   | TypeScript 类型检查 |
| `pnpm lint`   | oxlint 检查         |
| `pnpm format` | oxfmt 格式化        |

[rspack.config.ts](rspack.config.ts) 默认连接 `https://api.deepseek.com`。`createConfig({ apiOrigin, devServer })` 支持自定义 API 地址和开发服务器代理。

聊天默认使用 `deepseek-flash`，通过 Anthropic 兼容的 Messages / Files API 通信；语音使用 ASR / TTS 接口。请求使用搜索、链接阅读、标题和 `latest_reminder` 等扩展，接入服务需支持相应能力。附件实际支持的格式以服务端校验为准。

```text
src/
  app/                 应用布局、主题与全局样式
  components/          聊天、设置、Markdown 等界面组件
  i18n/                中英文文案
  services/
    api/               API 请求、SSE 与协议类型
    chat/              会话、消息分支、本地存储与历史导入导出
    markdown/          Markdown 解析、代码高亮与图表
    speech/            录音与朗读
  utils/               通用工具
```

## 许可证

[MIT](LICENSE)。构建会将许可证复制到 `dist/LICENSE`，分发时请一并保留；第三方依赖遵循各自的许可证。
