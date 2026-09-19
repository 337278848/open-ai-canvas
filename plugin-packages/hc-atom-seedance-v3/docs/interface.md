# HC-ATOM Seedance V3 接口字段

## 协议身份

- 插件 ID：`hc-atom-seedance-v3`
- Provider ID：`hc-atom-seedance-v3-video`
- 能力：`video`
- 默认 Base URL：`https://admin-aigc.fzyinghe.com`
- 鉴权：`Authorization: Bearer <API_KEY>`
- 创建：`POST /v3/video/tasks`
- 查询：`GET /v3/video/tasks/{taskId}`

## 配置字段

| 字段 | 类型 | 必填 | 含义 |
| --- | --- | --- | --- |
| `apiKey` | secret | 是 | HC-ATOM API Key |

## 请求字段

| 统一字段 | 上游字段 | 说明 |
| --- | --- | --- |
| `model` | `model` | 例如 `doubao-seedance-2.5` |
| `prompt` | `content[].text` | 文本提示词 |
| `images` | `content[].image_url` | 参考图，支持 `first_frame`、`last_frame`、`reference_image` |
| `videos` | `content[].video_url` | 参考视频，使用 `reference_video` |
| `audios` | `content[].audio_url` | 参考音频，使用 `reference_audio` |
| `resolution` | `resolution` | 例如 `480p`、`720p` |
| `aspectRatio` | `ratio` | 例如 `16:9`、`9:16`、`adaptive` |
| `duration` | `duration` | 视频秒数；默认 5 秒 |
| `generateAudio` | `generate_audio` | 是否生成同步音频 |
| `watermark` | `watermark` | 是否添加水印 |

参考媒体使用公网 HTTP/HTTPS URL 或 HC-ATOM 账户下的 `asset://asset-*` 素材 ID。

## 数量限制

- 图片最多 30 张
- 视频最多 10 个
- 音频最多 10 个
- 所有参考媒体总数最多 50 个

## 响应映射

创建响应示例：

```json
{
  "id": "cgt-20260724150000-abc12",
  "status": "queued",
  "model": "doubao-seedance-2.5"
}
```

查询成功响应示例：

```json
{
  "id": "cgt-20260724150000-abc12",
  "status": "succeeded",
  "content": {
    "video_url": "https://example.com/generated-video.mp4",
    "last_frame_url": "https://example.com/last-frame.png"
  },
  "usage": {
    "completion_tokens": 60682,
    "total_tokens": 60682
  }
}
```

状态由宿主统一归一化：

- `queued`、`submitted`：等待
- `running`、`processing`：处理中
- `succeeded`、`completed`：成功
- `failed`、`expired`、`cancelled`：失败或取消

视频结果是临时地址，宿主会下载并转存到项目资源。

<!-- YINGCE_MANIFEST_CONTRACT_START -->
## Manifest 完整接口定义

以下 JSON 与插件包内实际 `manifest.json` 逐字段一致，覆盖插件身份、权限、配置、鉴权、参数、校验、创建、Agent、查询、取消、结果下载、响应和 Agent 响应映射。`documentation` 字段的值就是当前完整文档；为避免文档在自身内部无限递归，JSON 中仅用等义占位文本表示正文。

```json
{
  "apiVersion": "yingce.plugin/v2",
  "id": "hc-atom-seedance-v3",
  "name": "HC-ATOM Seedance V3",
  "version": "1.0.0",
  "author": "HC-ATOM / 影策",
  "description": "HC-ATOM Seedance V3 视频任务协议。",
  "permissions": [
    "generation.run",
    "media.read"
  ],
  "configuration": {
    "fields": [
      {
        "name": "apiKey",
        "type": "secret",
        "label": "API Key",
        "required": true
      }
    ]
  },
  "contributes": {
    "providers": [
      {
        "id": "hc-atom-seedance-v3-video",
        "label": "HC-ATOM Seedance V3 视频",
        "capabilities": [
          "video"
        ],
        "scopes": [
          "admin.system-channel",
          "user.custom-channel",
          "canvas",
          "creation",
          "agent"
        ],
        "baseUrl": "https://admin-aigc.fzyinghe.com",
        "requiresPublicMediaUrls": true,
        "auth": {
          "type": "bearer",
          "field": "apiKey"
        },
        "parameters": [
          {
            "name": "model",
            "type": "string",
            "required": true,
            "mapping": "model",
            "description": "HC-ATOM Seedance 模型编码。"
          },
          {
            "name": "prompt",
            "type": "string",
            "required": true,
            "mapping": "content[type=text].text",
            "description": "视频提示词。"
          },
          {
            "name": "images",
            "type": "media[]",
            "required": false,
            "mapping": "content[type=image_url]",
            "description": "参考图片，支持首帧、尾帧和参考图角色。"
          },
          {
            "name": "videos",
            "type": "media[]",
            "required": false,
            "mapping": "content[type=video_url]",
            "description": "参考视频。"
          },
          {
            "name": "audios",
            "type": "media[]",
            "required": false,
            "mapping": "content[type=audio_url]",
            "description": "参考音频。"
          },
          {
            "name": "aspectRatio",
            "type": "string",
            "required": false,
            "mapping": "ratio",
            "description": "视频宽高比。"
          },
          {
            "name": "resolution",
            "type": "string",
            "required": false,
            "mapping": "resolution",
            "description": "视频分辨率档位。"
          },
          {
            "name": "duration",
            "type": "integer",
            "required": false,
            "mapping": "duration",
            "description": "视频时长秒数。"
          },
          {
            "name": "generateAudio",
            "type": "boolean",
            "required": false,
            "mapping": "generate_audio",
            "description": "是否生成同步音频。"
          },
          {
            "name": "watermark",
            "type": "boolean",
            "required": false,
            "mapping": "watermark",
            "description": "是否添加水印。"
          }
        ],
        "validations": [
          {
            "assert": {
              "$lte": [
                {
                  "$len": {
                    "$ref": "request.images"
                  }
                },
                30
              ]
            },
            "message": "HC-ATOM Seedance 最多支持 30 张参考图片"
          },
          {
            "assert": {
              "$lte": [
                {
                  "$len": {
                    "$ref": "request.videos"
                  }
                },
                10
              ]
            },
            "message": "HC-ATOM Seedance 最多支持 10 个参考视频"
          },
          {
            "assert": {
              "$lte": [
                {
                  "$len": {
                    "$ref": "request.audios"
                  }
                },
                10
              ]
            },
            "message": "HC-ATOM Seedance 最多支持 10 个参考音频"
          },
          {
            "assert": {
              "$lte": [
                {
                  "$add": [
                    {
                      "$len": {
                        "$ref": "request.images"
                      }
                    },
                    {
                      "$len": {
                        "$ref": "request.videos"
                      }
                    },
                    {
                      "$len": {
                        "$ref": "request.audios"
                      }
                    }
                  ]
                },
                50
              ]
            },
            "message": "HC-ATOM Seedance 参考媒体总数最多为 50 个"
          }
        ],
        "create": {
          "method": "POST",
          "path": "/v3/video/tasks",
          "contentType": "application/json",
          "body": {
            "model": {
              "$ref": "request.model"
            },
            "content": {
              "$concatArrays": [
                [
                  {
                    "type": "text",
                    "text": {
                      "$ref": "request.prompt"
                    }
                  }
                ],
                {
                  "$map": {
                    "from": {
                      "$sortByOrder": {
                        "$ref": "request.images"
                      }
                    },
                    "as": "media",
                    "in": {
                      "type": "image_url",
                      "image_url": {
                        "url": {
                          "$ref": "media.value"
                        }
                      },
                      "role": {
                        "$coalesce": [
                          {
                            "$ref": "media.role"
                          },
                          "reference_image"
                        ]
                      }
                    }
                  }
                },
                {
                  "$map": {
                    "from": {
                      "$sortByOrder": {
                        "$ref": "request.videos"
                      }
                    },
                    "as": "media",
                    "in": {
                      "type": "video_url",
                      "video_url": {
                        "url": {
                          "$ref": "media.value"
                        }
                      },
                      "role": {
                        "$coalesce": [
                          {
                            "$ref": "media.role"
                          },
                          "reference_video"
                        ]
                      }
                    }
                  }
                },
                {
                  "$map": {
                    "from": {
                      "$sortByOrder": {
                        "$ref": "request.audios"
                      }
                    },
                    "as": "media",
                    "in": {
                      "type": "audio_url",
                      "audio_url": {
                        "url": {
                          "$ref": "media.value"
                        }
                      },
                      "role": {
                        "$coalesce": [
                          {
                            "$ref": "media.role"
                          },
                          "reference_audio"
                        ]
                      }
                    }
                  }
                }
              ]
            },
            "resolution": {
              "$coalesce": [
                {
                  "$ref": "request.resolution"
                },
                "720p"
              ]
            },
            "ratio": {
              "$coalesce": [
                {
                  "$ref": "request.aspectRatio"
                },
                "16:9"
              ]
            },
            "duration": {
              "$if": {
                "condition": {
                  "$gt": [
                    {
                      "$ref": "request.duration"
                    },
                    0
                  ]
                },
                "then": {
                  "$ref": "request.duration"
                },
                "else": 5
              }
            },
            "generate_audio": {
              "$ref": "request.generateAudio"
            },
            "watermark": {
              "$ref": "request.watermark"
            }
          }
        },
        "poll": {
          "method": "GET",
          "path": "/v3/video/tasks/{{taskId}}",
          "contentType": "application/json"
        },
        "response": {
          "taskId": {
            "$coalesce": [
              {
                "$ref": "response.id"
              },
              {
                "$ref": "response.task_id"
              },
              {
                "$ref": "response.taskId"
              },
              {
                "$ref": "response.data.id"
              },
              {
                "$ref": "taskId"
              }
            ]
          },
          "status": {
            "$coalesce": [
              {
                "$ref": "response.status"
              },
              {
                "$ref": "response.state"
              },
              {
                "$ref": "response.data.status"
              },
              "pending"
            ]
          },
          "message": {
            "$coalesce": [
              {
                "$ref": "response.error.message"
              },
              {
                "$ref": "response.message"
              },
              {
                "$ref": "response.error"
              },
              {
                "$ref": "response.fail_reason"
              }
            ]
          },
          "videos": {
            "$coalesce": [
              {
                "$ref": "response.content.video_url"
              },
              {
                "$ref": "response.content.videoUrl"
              },
              {
                "$ref": "response.video_url"
              },
              {
                "$ref": "response.videoUrl"
              },
              {
                "$ref": "response.data.video_url"
              },
              {
                "$ref": "response.result.video_url"
              }
            ]
          },
          "usage": {
            "$ref": "response.usage"
          },
          "errorPaths": [
            "error.code"
          ],
          "resultEphemeral": true
        }
      }
    ]
  },
  "documentation": "<当前插件的完整 documentation，由 README.md 与 docs/interface.md 拼接而成；为避免 JSON 递归，此处不重复展开正文。>"
}
```
<!-- YINGCE_MANIFEST_CONTRACT_END -->
