// 画布 Agent 面向用户的错误文案净化。
//
// 模型、工具与上游媒体服务的原始错误可能包含密钥、内部地址或冗长堆栈；
// 这里统一转换成可读、可展示的中文说明，原始错误只进诊断日志。

package app

import (
	"errors"
	"strings"
	"unicode/utf8"

	"infinite-canvas/backend/internal/model"
)

// Only expose known failure categories; raw provider errors can contain URLs and credentials.
func cloudAgentModelFailure(task *model.Task) (string, string) {
	detail, reason := "模型任务未成功", "model_task_failed"
	if diagnostic := taskExecutionDiagnostic(task); diagnostic != nil && diagnostic.Code == string(ReasonUpstreamDNSFailed) {
		return "模型服务域名解析失败，请检查渠道域名和后端 DNS 配置；本轮已停止。请在任务中心检查模型任务 " + task.ID, string(ReasonUpstreamDNSFailed)
	}
	raw := strings.ToLower(task.Error)
	switch {
	case strings.Contains(task.Error, "没有返回内容"):
		// 思考模型的典型失败：整个输出预算被推理吃掉，正文与工具调用皆空。
		detail, reason = "上游连续返回空内容（通常是思考占满输出预算）；已自动关思考并放大预算重试仍失败，建议换用非思考模型或调小上下文", "model_empty_output"
	case strings.Contains(task.Error, cloudAgentStepTimeoutError):
		detail, reason = "单步模型调用超过执行时限仍未返回（长思考或上下文过大时常见）；已自动关思考重试仍超时，可在管理端调大 Agent 单步超时", "model_step_timeout"
	case strings.Contains(raw, "connection reset by peer"):
		detail, reason = "模型连接被对端或中间网络设备重置", "model_connection_reset"
	case strings.Contains(raw, "unexpected eof"):
		detail, reason = "模型连接意外中断", "model_connection_interrupted"
	case strings.Contains(raw, "timeout"), strings.Contains(raw, "deadline exceeded"):
		detail, reason = "模型请求超时", "model_request_timeout"
	case strings.Contains(raw, "connection refused"):
		detail, reason = "无法连接模型服务（连接被拒绝）", "model_connection_refused"
	case strings.Contains(task.Error, "过载") || strings.Contains(task.Error, "暂时不可用") || strings.Contains(raw, "overloaded"):
		detail, reason = "上游模型服务暂时过载或不可用", "model_overloaded"
	}
	// Task.Error 在正常链路里已是归类后的固定文案，但这里不能直接回显：该字段
	// 在历史数据或异常路径上可能带着上游正文。只有能再次命中固定类目的文本
	// 才采用，让它覆盖笼统的"模型任务未成功"，同时保持不回显原始错误。
	if categorized, ok := providerPayloadErrorCategory(task.Error); ok {
		detail = categorized
	}
	return detail + "；本轮已停止。请在任务中心检查模型任务 " + task.ID, reason
}

func cloudAgentSafeToolError(err error) string {
	if err == nil {
		return ""
	}
	var readLoopErr *cloudAgentReadLoopError
	if errors.As(err, &readLoopErr) {
		return readLoopErr.Error()
	}
	var appErr *AppError
	if errors.As(err, &appErr) && appErr != nil {
		message := strings.TrimSpace(appErr.Message)
		if cloudAgentSafeUserMessage(message) {
			return message
		}
	}
	return "工具执行失败，请检查输入或稍后重试"
}

func cloudAgentSafeUserMessage(message string) bool {
	if message == "" || !utf8.ValidString(message) || strings.ContainsAny(message, "\x00\r\n") || utf8.RuneCountInString(message) > 240 {
		return false
	}
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"http://", "https://", "ftp://", "file://", "authorization", "cookie", "secret", "token", "api_key", "apikey", "x-api-key",
		"/var/", "/tmp/", "\\", "stack trace", "traceback", " at ", "sql:", "sqlite", "postgres",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// cloudAgentSafeMediaTaskError preserves a short, user-facing task diagnostic
// while refusing provider details that commonly contain URLs, credentials, or
// internal request metadata. Task.Error is not a safe presentation field.
func cloudAgentSafeMediaTaskError(task *model.Task) string {
	if task == nil || strings.TrimSpace(task.Error) == "" {
		return "媒体任务未成功"
	}
	detail := strings.TrimSpace(task.Error)
	if detail == "" || !utf8.ValidString(detail) || strings.ContainsAny(detail, "\r\n\x00") {
		return "媒体任务未成功"
	}
	if categorized, ok := providerPayloadErrorCategory(detail); ok {
		return categorized
	}
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "unexpected eof"):
		return "模型连接意外中断，请稍后重试"
	case strings.Contains(lower, "connection reset by peer"):
		return "模型连接被中断，请稍后重试"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return "模型服务响应超时，请稍后重试"
	}
	for _, marker := range []string{
		"http://", "https://", "ftp://", "authorization", "cookie", "secret", "token", "api_key", "apikey", "x-api-key",
	} {
		if strings.Contains(lower, marker) {
			return "媒体任务未成功"
		}
	}
	runes := []rune(detail)
	if len(runes) > 240 {
		detail = string(runes[:240]) + "…"
	}
	return detail
}
