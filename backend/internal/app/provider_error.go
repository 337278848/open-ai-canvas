package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

const contentModerationErrorCode = "sensitive_words_detected"

const contentModerationRetryMessage = "内容审核未通过，请修改提示词后重新生成；原任务不能直接重试"

// 只提取供应商明确返回的错误码和短消息，避免把完整响应或用户输入复制到调用日志。
func providerFailureDetails(payload map[string]any) (string, string) {
	candidates := make([]map[string]any, 0, 3)
	for _, key := range []string{"error", "data"} {
		if nested, ok := payload[key].(map[string]any); ok {
			candidates = append(candidates, nested)
		}
	}
	// 内层通常是供应商业务错误，外层 code 可能只是 HTTP 包装码。
	candidates = append(candidates, payload)
	code := ""
	message := ""
	for _, candidate := range candidates {
		if code == "" {
			code = normalizedProviderErrorCode(candidate["code"])
		}
		if message == "" {
			message = strings.TrimSpace(stringField(candidate, "message"))
			if message == "" {
				message = strings.TrimSpace(stringField(candidate, "msg"))
			}
		}
	}
	return code, truncateRunes(message, 500)
}

func providerResponseBusinessFailure(responseBody []byte) (string, string, bool) {
	if len(responseBody) == 0 {
		return "", "", false
	}
	var payload map[string]any
	if json.Unmarshal(responseBody, &payload) != nil {
		// 流式协议用 HTTP 200 承载业务失败：正文里是 SSE 帧，真正的错误在
		// `event: error` 的 data 中。不解析它，这次调用就会被记成成功，
		// 让任务失败原因在调用日志里消失，并把费用卡在"待核对"。
		return providerStreamBusinessFailure(responseBody)
	}
	return providerPayloadBusinessFailure(payload)
}

// providerStreamBusinessFailure 从 SSE 正文里提取上游显式错误帧。
// 只认 data 里带 error 对象或业务失败 code 的帧；正常增量帧不改判定。
func providerStreamBusinessFailure(responseBody []byte) (string, string, bool) {
	code := ""
	message := ""
	sawFrame := false
	for _, frame := range sseFrameBoundaryPattern.Split(string(responseBody), -1) {
		dataLines := make([]string, 0, 1)
		for _, line := range strings.Split(strings.ReplaceAll(frame, "\r\n", "\n"), "\n") {
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		raw := strings.TrimSpace(strings.Join(dataLines, "\n"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		sawFrame = true
		// 只有真正的 error 载荷才算失败：message_start 之类正常帧也走同一路径。
		if _, ok := payload["error"].(map[string]any); ok {
			frameCode, frameMessage := providerFailureDetails(payload)
			if code == "" {
				code = frameCode
			}
			if frameMessage != "" {
				message = frameMessage
			}
			continue
		}
		if frameCode, frameMessage, failed := providerPayloadBusinessFailure(payload); failed {
			if code == "" {
				code = frameCode
			}
			if frameMessage != "" {
				message = frameMessage
			}
		}
	}
	if !sawFrame || message == "" {
		return "", "", false
	}
	return code, message, true
}

func providerPayloadBusinessFailure(payload map[string]any) (string, string, bool) {
	if code, message, failed := providerBusinessFailure(payload); failed {
		return code, message, true
	}
	// DashScope 业务失败在 output 内
	if output, ok := payload["output"].(map[string]any); ok {
		return providerBusinessFailure(output)
	}
	return "", "", false
}

func providerBusinessFailure(payload map[string]any) (string, string, bool) {
	if errorValue, ok := payload["error"].(map[string]any); ok {
		code, message := providerFailureDetails(map[string]any{"error": errorValue})
		if code != "" || message != "" {
			return code, message, true
		}
	}

	code := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["code"])))
	if code != "" && code != "0" &&
		code != "success" && code != "succeeded" &&
		code != "ok" && code != "<nil>" {
		code, message := providerFailureDetails(payload)
		return code, message, true
	}

	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["task_status"])))
	switch status {
	case "failed", "failure", "error", "expired":
		code, message := providerFailureDetails(payload)
		if code == "" {
			code = "task_failed"
		}
		return code, message, true
	}

	return "", "", false
}

func normalizedProviderErrorCode(value any) string {
	var code string
	switch current := value.(type) {
	case string:
		code = current
	case fmt.Stringer:
		code = current.String()
	case float64:
		if current != 0 {
			code = fmt.Sprintf("%g", current)
		}
	case int:
		if current != 0 {
			code = fmt.Sprintf("%d", current)
		}
	case int64:
		if current != 0 {
			code = fmt.Sprintf("%d", current)
		}
	}
	code = strings.TrimSpace(code)
	if code == "0" {
		return ""
	}
	return truncateRunes(code, 80)
}

func isContentModerationFailure(value string) bool {
	return strings.Contains(strings.ToLower(value), contentModerationErrorCode)
}
