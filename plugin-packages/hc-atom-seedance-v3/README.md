# HC-ATOM Seedance V3

该目录是 HC-ATOM（`admin-aigc.fzyinghe.com`）Seedance V3 视频任务协议的官方声明式插件源码。

它适配 HC-ATOM 文档中的：

- `POST /v3/video/tasks`
- `GET /v3/video/tasks/{taskId}`

默认模型使用 `doubao-seedance-2.5`。模型 ID 会原样透传，管理员也可以在系统渠道中配置同一协议下的其他 Seedance 模型。

完整字段、参数限制和响应映射见 [docs/interface.md](docs/interface.md)。
