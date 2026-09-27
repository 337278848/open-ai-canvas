package app

import (
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestSanitizeAdminAPICallLogHidesProviderDetails(t *testing.T) {
	log := sanitizeAdminAPICallLog(model.ApiCallLog{
		Source:            "backend-provider",
		APIFormat:         "volcengine",
		Path:              "/api/v3/contents/generations/tasks/provider-task",
		ProviderStatus:    "processing",
		ProviderRequestID: "provider-task-123",
		UpstreamURL:       "https://provider.example/api/v3?token=secret",
		RequestBody:       `{"model":"provider-model","api_key":"secret"}`,
		ResponseBody:      `{"provider":"example","id":"provider-task-123"}`,
		ErrorCode:         "provider_error",
		Error:             "请求 https://provider.example/error?token=secret 失败",
	})

	for name, value := range map[string]string{
		"source":              log.Source,
		"api format":          log.APIFormat,
		"provider status":     log.ProviderStatus,
		"provider request id": log.ProviderRequestID,
		"upstream URL":        log.UpstreamURL,
		"request body":        log.RequestBody,
		"response body":       log.ResponseBody,
	} {
		if value != "" {
			t.Fatalf("%s leaked through admin API view: %q", name, value)
		}
	}
	if log.Path != "平台内部请求" {
		t.Fatalf("path = %q, want product-facing placeholder", log.Path)
	}
	if strings.Contains(log.Error, "https://") || strings.Contains(log.Error, "token=secret") {
		t.Fatalf("error still exposes provider transport details: %q", log.Error)
	}
}
