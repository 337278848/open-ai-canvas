<p align="center">
  <img src="web/public/logo.svg" width="88" alt="拼好布 logo">
</p>

<h1 align="center">拼好布</h1>

<p align="center">让一个故事，从文字走向银幕</p>

<p align="center">
  <a href="https://github.com/337278848/open-ai-canvas">GitHub</a> ·
  <a href="docs/content/docs/overview/features.mdx">功能</a> ·
  <a href="docs/content/docs/overview/quick-start.mdx">文档</a> ·
  <a href="SECURITY.md">安全策略</a>
</p>

拼好布是一个 AI 影视与短剧创作工作台：用自由画布组织创作，用结构化工作流管理剧本、角色、场景和分镜，并通过统一的任务系统完成图片、视频、音频与文本生成。

> 项目仍在快速开发，数据结构和外部接口可能调整。默认适合个人、本地或可信环境部署；未经安全配置，不要直接作为公网多人服务使用。

在线演示：[http://tv.pinhaoai.help/](http://tv.pinhaoai.help/)

## 核心能力

- **自由画布**：节点、连线、框选、缩放、小地图、撤销重做、导入导出和只读分享。
- **影视创作工作流**：剧本、角色、场景、风格板、参考素材、结构化分镜和 3D 导演台。
- **多媒体生成**：文本、图片、视频、音频任务，支持参考图、首尾帧、运镜、续写、局部修改和批量生成。
- **任务与素材管理**：异步队列、进度与日志、取消/重试、素材库、资源引用校验和登录后的跨设备同步。
- **时间线剪辑**：片段编排、拆分、修剪、字幕转写和服务端成片导出，并支持插件化编辑面板。
- **画布助手**：支持持久化对话、流式事件回放、分镜协作和生成审批；收费操作在确认前可以调整模型与生成规格。
- **创作技能**：内置影视剧作工坊等技能，支持按创作需要安装、管理和收藏技能。
- **独立生成工作台**：提供图片与视频工作台，支持工具预设、参考素材与任务结果管理。
- **账户与安全**：提供个人资料、头像设置与登录记录，便于管理账号访问。
- **管理与渠道**：系统渠道、逻辑模型、用量/积分、功能开关、对象存储、响应拦截和管理后台。

完整功能以[功能清单](docs/content/docs/overview/features.mdx)为准。

## 快速开始

### 环境要求

- [Bun](https://bun.sh/)：前端和文档站
- [Go 1.25](https://go.dev/)：后端
- Docker Compose：仅在使用容器开发或部署时需要

### 宿主机启动

```bash
git clone https://github.com/337278848/open-ai-canvas.git
cd open-ai-canvas

# 使用 Git 忽略的目录保存本地开发数据和缓存
mkdir -p .local/project-workbench-debug .local/cache/go-build .local/cache/go-mod

# 终端一：后端
cd backend
CANVAS_BACKEND_ADDR=127.0.0.1:8080 \
CANVAS_BACKEND_DATA_DIR=../.local/project-workbench-debug \
GOCACHE=../.local/cache/go-build \
GOMODCACHE=../.local/cache/go-mod \
go run ./cmd/server

# 终端二：前端
cd ../web
bun install --frozen-lockfile
bun run dev
```

打开 <http://localhost:3000>。首次使用时注册管理员账号，并在设置中配置模型渠道。前端默认将 `/api` 代理到 `http://127.0.0.1:8080`；如需修改代理目标，可设置 `VITE_API_PROXY_TARGET`。

Windows PowerShell 用户可在仓库根目录执行：

```powershell
.\scripts\start-local.ps1
```

### Docker 开发与本地构建

源码热更新：

```bash
LOCAL_UID=$(id -u) LOCAL_GID=$(id -g) \
  docker compose -f docker-compose.dev.yml up --build
```

本地构建并运行 release 镜像：

```bash
docker compose -f docker-compose.local.yml up -d --build
```

默认前端端口为 `3000`、后端端口为 `8080`；端口冲突时可通过 `CANVAS_WEB_HOST_PORT` 和 `CANVAS_BACKEND_HOST_PORT` 覆盖。

更多本地开发说明（包括时间线字幕转写）见[本地开发文档](docs/content/docs/backend/local-development.mdx)。

## 架构概览

```text
浏览器（web/）
  ├─ React 工作区、画布、任务中心和素材库
  ├─ Zustand / localForage 本地状态与降级缓存
  └─ 登录态 API、资源请求和 SSE
          │
          ▼
后端（backend/）
  ├─ Gin handler -> service -> repository/model
  ├─ SQLite（本地）或 PostgreSQL + Redis（部署）
  ├─ 异步任务 worker、权限、资源存储和模型中转
  └─ provider / outbound -> 外部模型渠道
```

前端业务 API 统一经 `web/src/services/api/request.ts` 调用。生产环境由 Nginx 托管前端并代理后端，公网只需暴露 web 入口；SSE 仅在明确的流式路径关闭代理缓冲。

## 服务器部署

### 源码构建（推荐）

适用于 Linux 云服务器。脚本会安装 Docker、拉取源码、生成受保护的 `.env`，并启动 PostgreSQL、Redis、后端和网页：

```bash
curl -fsSL https://raw.githubusercontent.com/337278848/open-ai-canvas/main/scripts/install-server.sh | sudo bash
```

默认访问 `http://服务器IP:3000`。更新或排查：

```bash
cd /opt/open-ai-canvas
sudo docker compose --env-file .env \
  -f docker-compose.deploy.yml -f docker-compose.build.yml ps
sudo docker compose --env-file .env \
  -f docker-compose.deploy.yml -f docker-compose.build.yml logs -f --tail=200
```

### 使用 GHCR 镜像

不需要源码时，可使用镜像部署脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/337278848/open-ai-canvas/main/scripts/install-server-image.sh \
  | sudo env CANVAS_IMAGE_TAG=v1.6.7 bash
```

历史 `v1.2.9`、`v1.5.7`、`v1.5.7.1` 的 Host Updater 仍是 tag-based 格式，必须显式设置 `CANVAS_SKIP_HOST_UPDATER=1`；脚本会在写入部署文件前拒绝隐式安装旧更新器。脚本会先按指定 Release 拉取镜像，再解析并写入 `CANVAS_BACKEND_IMAGE`、`CANVAS_WEB_IMAGE` 的 GHCR digest；生产 Compose 不接受缺失 digest 的默认镜像或 `latest`。更新流程、数据库迁移、备份和回退说明见[系统更新文档](docs/content/docs/backend/system-update.mdx)。

## 安全边界

- 首次管理员注册应在受控网络完成，公网部署保持 `CANVAS_REGISTRATION_ENABLED=false`。
- 设置准确的 `CANVAS_CORS_ORIGINS`，不要在公网使用 `*`；使用 HTTPS 并正确转发代理头。
- 后端默认拒绝本机、私网和链路本地模型地址。开发时只通过 `CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS` 精确放行可信主机，不要使用全量放行开关。
- 用户 API Key 不应出现在 URL、日志、错误上报或服务端长期明文存储中；只在可信部署和 HTTPS 链路中使用真实密钥。
- 后端 `8080` 应留在 Compose 网络内，不要直接暴露到公网；限制 `.env`、数据库、上传目录、备份和 `.settings-key` 的权限。
- 媒体资源可使用后端数据目录、阿里云 OSS 或腾讯云 COS；删除素材前会检查业务引用。

安全问题请按 [`SECURITY.md`](SECURITY.md) 报告，不要在公开 Issue 中粘贴密钥、Cookie、数据库或生产日志。

## 文档与验证

### 文档导航

- [快速开始](docs/content/docs/overview/quick-start.mdx)
- [功能清单](docs/content/docs/overview/features.mdx)
- [代码功能地图](docs/content/docs/backend/code-map.mdx)
- [本地开发](docs/content/docs/backend/local-development.mdx)
- [数据库结构](docs/content/docs/backend/backend-database.mdx)
- [画布操作手册](docs/content/docs/canvas/canvas-node-manual.mdx)
- [插件系统](docs/content/docs/plugins/plugin-system.mdx)
- [待办与待测试](docs/content/docs/progress/todo.mdx) · [待测试清单](docs/content/docs/progress/pending-test.mdx)
- [更新日志](CHANGELOG.md) · [贡献指南](CONTRIBUTING.md) · [上游声明](NOTICE)

### 验证命令

按改动范围运行最小验证：

```bash
# 前端
cd web && bun run lint && bun run build

# 后端
cd backend && go test ./...

# 文档站
cd docs && bun run types:check
```

## 许可证和上游

本项目采用 [MIT](LICENSE) 协议。基于 [basketikun/infinite-canvas](https://github.com/basketikun/infinite-canvas) 的早期版本进行二次开发，上游作者和贡献者保留其对应代码的权利与署名。

---

## 产品团队

拼好布由产品、工程、创作与测试团队持续维护。团队成员信息按内部渠道维护，不在产品仓库中公开联系方式。
