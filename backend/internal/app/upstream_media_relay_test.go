package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newRelayTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Resource{}, &model.SystemSetting{}, &model.UserOSSSetting{}, &model.StorageLocation{}, &model.AdminAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db), dataDir: t.TempDir()}, db
}

func seedRelayResource(t *testing.T, svc *Service, resource model.Resource) model.Resource {
	t.Helper()
	resource.Provider = "local"
	resource.Status = model.ResourceStatusReady
	resource.ObjectKey = "users/" + resource.UserID + "/image/" + resource.ID + ".png"
	if resource.MimeType == "" {
		resource.MimeType = "image/png"
	}
	if resource.Kind == "" {
		resource.Kind = "image"
	}
	if err := writeLocalResourceObject(localResourcePath(svc, &resource), bytes.NewReader([]byte("relay-image-bytes"))); err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.CreateResource(&resource); err != nil {
		t.Fatal(err)
	}
	return resource
}

// localResourcePath 复用资源写入约定，避免测试里重复拼接数据目录结构。
func localResourcePath(svc *Service, resource *model.Resource) string {
	return svc.dataDir + "/resources/" + resource.ObjectKey
}

// TestUpstreamProviderMediaURLRelaysLocalStorageWhenNoPublicBaseURL 覆盖本次修复的核心动机：
// 本地存储且未配置服务器公网地址时，上游参考素材应改用图床中继地址，而不是直接报错。
func TestUpstreamProviderMediaURLRelaysLocalStorageWhenNoPublicBaseURL(t *testing.T) {
	svc, _ := newRelayTestService(t)
	resource := seedRelayResource(t, svc, model.Resource{ID: "resource-relay-1", UserID: "user-1"})

	var uploaded bool
	svc.relayUploadOverride = func(_ string, fileName string, mimeType string, data []byte) (string, error) {
		uploaded = true
		if fileName != "resource-relay-1.png" {
			t.Fatalf("relay filename = %q, want resource-relay-1.png", fileName)
		}
		if mimeType != "image/png" {
			t.Fatalf("relay mimeType = %q, want image/png", mimeType)
		}
		if string(data) != "relay-image-bytes" {
			t.Fatalf("relay payload = %q", data)
		}
		return "https://example.com/relayed.png", nil
	}

	value, err := svc.upstreamProviderMediaURL("user-1", &resource, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("upstreamProviderMediaURL() error = %v", err)
	}
	if !uploaded {
		t.Fatal("relay upload was not attempted")
	}
	if value != "https://example.com/relayed.png" {
		t.Fatalf("upstreamProviderMediaURL() = %q", value)
	}
	// 中继结果必须落库，供后续任务在同一存活期内复用。
	stored, err := svc.repo.Resource(resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RelayURL != "https://example.com/relayed.png" || stored.RelayExpiresAt == nil {
		t.Fatalf("relay result not persisted: %#v", stored)
	}
}

// TestUpstreamProviderMediaURLReusesUnexpiredRelay 同一素材在存活期内重复请求不应反复上传。
func TestUpstreamProviderMediaURLReusesUnexpiredRelay(t *testing.T) {
	svc, _ := newRelayTestService(t)
	resource := seedRelayResource(t, svc, model.Resource{ID: "resource-relay-2", UserID: "user-1"})
	expiresAt := time.Now().Add(time.Hour)
	if err := svc.repo.SaveResourceRelayURL(resource.ID, "https://example.com/cached.png", expiresAt); err != nil {
		t.Fatal(err)
	}
	resource.RelayURL = "https://example.com/cached.png"
	resource.RelayExpiresAt = &expiresAt

	svc.relayUploadOverride = func(string, string, string, []byte) (string, error) {
		t.Fatal("relay upload should be reused instead of re-uploaded")
		return "", nil
	}
	value, err := svc.upstreamProviderMediaURL("user-1", &resource, time.Now().Add(time.Hour))
	if err != nil || value != "https://example.com/cached.png" {
		t.Fatalf("upstreamProviderMediaURL() = %q, %v", value, err)
	}
}

// TestUpstreamProviderMediaURLPrefersOwnStorageWhenPublicBaseURLConfigured
// 部署自备公网地址时不得把用户素材交给第三方图床，商用部署依赖这一条。
func TestUpstreamProviderMediaURLPrefersOwnStorageWhenPublicBaseURLConfigured(t *testing.T) {
	svc, _ := newRelayTestService(t)
	svc.publicBaseURLOverride = "https://canvas.example.com"
	resource := seedRelayResource(t, svc, model.Resource{ID: "resource-relay-3", UserID: "user-1"})

	svc.relayUploadOverride = func(string, string, string, []byte) (string, error) {
		t.Fatal("relay must not run when the deployment has its own public address")
		return "", nil
	}
	// 中继被跳过后走的是自身签名链路；该链路仍按部署安全策略校验主机可解析性，
	// 因此这里只断言"没有交给第三方图床"，不断言 DNS 结果。
	if _, err := svc.upstreamProviderMediaURL("user-1", &resource, time.Now().Add(time.Hour)); err != nil {
		if strings.Contains(err.Error(), "图床") {
			t.Fatalf("relay should have been skipped, got %v", err)
		}
	}
}

// TestUpstreamProviderMediaURLLeavesObjectStorageUntouched 对象存储资源继续走原有签名链路。
func TestUpstreamProviderMediaURLLeavesObjectStorageUntouched(t *testing.T) {
	svc, _ := newRelayTestService(t)
	settingJSON, _ := json.Marshal(ossSettingValue{
		Enabled: true, Provider: "aliyun", Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		AccessKeyID: "access-id", AccessKeySecret: "secret-value",
	})
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: ossSettingKey, ValueJSON: string(settingJSON)}); err != nil {
		t.Fatal(err)
	}
	resource := model.Resource{
		ID: "resource-relay-oss", UserID: "user-1", Kind: "image", Status: model.ResourceStatusReady,
		Provider: "aliyun", Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "private-bucket",
		ObjectKey: "users/user-1/image/direct.png", MimeType: "image/png",
	}
	if err := svc.repo.CreateResource(&resource); err != nil {
		t.Fatal(err)
	}
	svc.relayUploadOverride = func(string, string, string, []byte) (string, error) {
		t.Fatal("object storage resources must not use the temporary image host")
		return "", nil
	}
	value, err := svc.upstreamProviderMediaURL("user-1", &resource, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("upstreamProviderMediaURL() error = %v", err)
	}
	if !strings.Contains(value, "Signature=") {
		t.Fatalf("upstreamProviderMediaURL() = %q, want OSS signed URL", value)
	}
}

// TestUpstreamProviderMediaURLFallsBackToSignedURLErrorWhenRelayFails
// 图床全部失败且自身也没有公网地址时，仍需返回可直接处理的资源地址原因。
func TestUpstreamProviderMediaURLFallsBackToSignedURLErrorWhenRelayFails(t *testing.T) {
	svc, _ := newRelayTestService(t)
	resource := seedRelayResource(t, svc, model.Resource{ID: "resource-relay-4", UserID: "user-1"})
	svc.relayUploadOverride = func(string, string, string, []byte) (string, error) {
		return "", errors.New("all providers failed")
	}
	_, err := svc.upstreamProviderMediaURL("user-1", &resource, time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("expected error when both relay and own address are unavailable")
	}
	if !strings.Contains(err.Error(), "服务器访问地址") {
		t.Fatalf("error should surface the actionable storage reason, got %v", err)
	}
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Cause == nil {
		t.Fatal("relay failure cause should be retained for diagnostics")
	}
}

// TestUpstreamMediaRelayEnvDisablesThirdPartyHost 商用部署可用环境变量强制关闭图床中继。
func TestUpstreamMediaRelayEnvDisablesThirdPartyHost(t *testing.T) {
	t.Setenv("CANVAS_UPSTREAM_MEDIA_RELAY", "false")
	svc, _ := newRelayTestService(t)
	resource := seedRelayResource(t, svc, model.Resource{ID: "resource-relay-5", UserID: "user-1"})
	svc.relayUploadOverride = func(string, string, string, []byte) (string, error) {
		t.Fatal("relay must stay disabled when the deployment disables it")
		return "", nil
	}
	if _, err := svc.upstreamProviderMediaURL("user-1", &resource, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected signed URL error when relay is disabled")
	}
}

// TestDirectResourceURLUsesRelayForLocalStorage 浏览器侧视频渠道通过
// /resources/:id/oss-url 取参考素材地址，因此 DirectResourceURL 必须与 provider 链路一致。
func TestDirectResourceURLUsesRelayForLocalStorage(t *testing.T) {
	svc, _ := newRelayTestService(t)
	resource := seedRelayResource(t, svc, model.Resource{ID: "resource-relay-direct", UserID: "user-1"})
	svc.relayUploadOverride = func(string, string, string, []byte) (string, error) {
		return "https://example.com/browser-relay.png", nil
	}
	value, err := svc.DirectResourceURL("user-1", resource.ID)
	if err != nil {
		t.Fatalf("DirectResourceURL() error = %v", err)
	}
	if value != "https://example.com/browser-relay.png" {
		t.Fatalf("DirectResourceURL() = %q, want relayed URL", value)
	}
	// 归属校验必须保留：其他用户仍然读不到这个资源。
	if _, err := svc.DirectResourceURL("other-user", resource.ID); err == nil {
		t.Fatal("DirectResourceURL() allowed another user's resource")
	}
}

func TestNormalizeUpstreamMediaRelaySettingRejectsUnknownHosts(t *testing.T) {
	value := normalizeUpstreamMediaRelaySetting(upstreamMediaRelaySettingValue{
		Provider:          "unknown-host",
		FallbackProviders: []string{"uguu", "uguu", "not-a-host", "tmpfiles"},
		TTL:               "99h",
		TimeoutSeconds:    0,
		MaxMB:             0,
	})
	if value.Provider != upstreamRelayProviderLitterbox {
		t.Fatalf("provider = %q, want default litterbox", value.Provider)
	}
	if len(value.FallbackProviders) != 2 || value.FallbackProviders[0] != upstreamRelayProviderUguu || value.FallbackProviders[1] != upstreamRelayProviderTmpfiles {
		t.Fatalf("fallbacks = %#v, want deduplicated uguu+tmpfiles", value.FallbackProviders)
	}
	if value.TTL != "1h" || value.TimeoutSeconds != defaultUpstreamRelayTimeoutSeconds || value.MaxMB != defaultUpstreamRelayMaxMB {
		t.Fatalf("defaults not applied: %#v", value)
	}
}

func TestUpstreamRelayProviderChainDedupesPrimary(t *testing.T) {
	chain := upstreamRelayProviderChain(upstreamMediaRelaySettingValue{
		Provider:          upstreamRelayProviderUguu,
		FallbackProviders: []string{"uguu", "tmpfiles"},
	})
	if len(chain) != 2 || chain[0] != upstreamRelayProviderUguu || chain[1] != upstreamRelayProviderTmpfiles {
		t.Fatalf("chain = %#v", chain)
	}
}

// TestParseUpstreamRelayResponseConvertsTmpfilesToDirectLink
// tmpfiles 返回的是网页地址，必须换成直链，否则上游模型拿到的是 HTML。
func TestParseUpstreamRelayResponseConvertsTmpfilesToDirectLink(t *testing.T) {
	payload := []byte(`{"status":"success","data":{"url":"https://tmpfiles.org/12345/sample.png"}}`)
	value, err := parseUpstreamRelayResponse(upstreamRelayProviderTmpfiles, payload)
	if err != nil {
		t.Fatal(err)
	}
	if value != "https://tmpfiles.org/dl/12345/sample.png" {
		t.Fatalf("tmpfiles direct url = %q", value)
	}
}

func TestParseUpstreamRelayResponseParsesEachProvider(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: upstreamRelayProviderLitterbox, payload: "https://litter.catbox.moe/abc.png", want: "https://litter.catbox.moe/abc.png"},
		{name: upstreamRelayProviderUguu, payload: `{"success":true,"files":[{"url":"https://uguu.se/abc.png"}]}`, want: "https://uguu.se/abc.png"},
	}
	for _, test := range tests {
		value, err := parseUpstreamRelayResponse(test.name, []byte(test.payload))
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if value != test.want {
			t.Fatalf("%s: got %q, want %q", test.name, value, test.want)
		}
	}
}

func TestParseUpstreamRelayResponseRejectsNonHTTPSAndGarbage(t *testing.T) {
	// 上游模型无法读取明文 HTTP 或非链接响应，必须显式失败而不是把垃圾地址发出去。
	failures := []struct {
		name    string
		payload string
	}{
		{name: upstreamRelayProviderLitterbox, payload: "http://insecure.example.com/a.png"},
		{name: upstreamRelayProviderLitterbox, payload: "not a url"},
		{name: upstreamRelayProviderUguu, payload: `{"success":false,"files":[]}`},
		{name: upstreamRelayProviderTmpfiles, payload: `{"status":"error"}`},
		{name: upstreamRelayProviderUguu, payload: `{`},
	}
	for _, test := range failures {
		if _, err := parseUpstreamRelayResponse(test.name, []byte(test.payload)); err == nil {
			t.Fatalf("%s: payload %q should be rejected", test.name, test.payload)
		}
	}
}

// TestUploadToTemporaryImageHostUsesFallbackWhenPrimaryFails 主用图床不可用时自动降级到兜底图床。
func TestUploadToTemporaryImageHostUsesFallbackWhenPrimaryFails(t *testing.T) {
	// 本地上游测试服务器需要按部署约定精确放行，出站 SSRF 防护保持开启。
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	svc, _ := newRelayTestService(t)
	var attempted []string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, upstreamRelayProviderLitterbox)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempted = append(attempted, upstreamRelayProviderUguu)
		_, _ = w.Write([]byte(`{"success":true,"files":[{"url":"https://example.com/final.png"}]}`))
	}))
	defer fallback.Close()

	setting := normalizeUpstreamMediaRelaySetting(upstreamMediaRelaySettingValue{
		Provider: upstreamRelayProviderLitterbox, FallbackProviders: []string{upstreamRelayProviderUguu},
		TTL: "1h", TimeoutSeconds: 10, MaxMB: 20,
	})
	// 主用与兜底都指向本地上游，测试不接触真实第三方图床。
	svc.relayProviderOverride = map[string]upstreamRelayProviderSpec{
		upstreamRelayProviderLitterbox: {endpoint: primary.URL, supportsTTL: true},
		upstreamRelayProviderUguu:      {endpoint: fallback.URL},
	}

	value, err := svc.uploadToTemporaryImageHost(t.Context(), setting, "a.png", "image/png", []byte("x"))
	if err != nil {
		t.Fatalf("expected fallback to succeed: %v", err)
	}
	if value != "https://example.com/final.png" {
		t.Fatalf("relayed url = %q", value)
	}
	if len(attempted) != 2 || attempted[0] != upstreamRelayProviderLitterbox || attempted[1] != upstreamRelayProviderUguu {
		t.Fatalf("attempted providers = %#v, want primary then fallback", attempted)
	}
}

// TestUploadToTemporaryImageHostNamesEveryProviderWhenAllFail 全部失败时错误需列出每个图床原因。
func TestUploadToTemporaryImageHostNamesEveryProviderWhenAllFail(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	svc, _ := newRelayTestService(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()

	setting := normalizeUpstreamMediaRelaySetting(upstreamMediaRelaySettingValue{
		Provider: upstreamRelayProviderLitterbox, FallbackProviders: []string{upstreamRelayProviderUguu},
		TTL: "1h", TimeoutSeconds: 10, MaxMB: 20,
	})
	svc.relayProviderOverride = map[string]upstreamRelayProviderSpec{
		upstreamRelayProviderLitterbox: {endpoint: down.URL, supportsTTL: true},
		upstreamRelayProviderUguu:      {endpoint: down.URL},
	}

	_, err := svc.uploadToTemporaryImageHost(t.Context(), setting, "a.png", "image/png", []byte("x"))
	if err == nil {
		t.Fatal("expected aggregate failure")
	}
	if !strings.Contains(err.Error(), upstreamRelayProviderLitterbox) || !strings.Contains(err.Error(), upstreamRelayProviderUguu) {
		t.Fatalf("error should name both providers: %v", err)
	}
}

// TestBuildUpstreamRelayUploadMatchesProviderContracts 各图床的表单字段不能写错，否则会被上游拒绝。
func TestBuildUpstreamRelayUploadMatchesProviderContracts(t *testing.T) {
	setting := normalizeUpstreamMediaRelaySetting(upstreamMediaRelaySettingValue{
		Provider: upstreamRelayProviderLitterbox, FallbackProviders: nil, TTL: "12h", TimeoutSeconds: 30, MaxMB: 20,
	})
	tests := []struct {
		provider string
		fields   []string
	}{
		{provider: upstreamRelayProviderLitterbox, fields: []string{"reqtype", "time", "fileToUpload"}},
		{provider: upstreamRelayProviderUguu, fields: []string{"files[]"}},
		{provider: upstreamRelayProviderTmpfiles, fields: []string{"file"}},
	}
	for _, test := range tests {
		body, _, err := buildUpstreamRelayUpload(test.provider, upstreamRelayProviders[test.provider], setting, "a.png", "image/png", []byte("payload"))
		if err != nil {
			t.Fatalf("%s: %v", test.provider, err)
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range test.fields {
			if !strings.Contains(string(raw), `name="`+field+`"`) {
				t.Fatalf("%s: multipart body missing field %q", test.provider, field)
			}
		}
	}
}

// TestValidateUpstreamMediaRelaySettingRejectsOutOfRange 管理端写入必须拒绝越界配置。
func TestValidateUpstreamMediaRelaySettingRejectsOutOfRange(t *testing.T) {
	valid := defaultUpstreamMediaRelaySetting()
	if err := validateUpstreamMediaRelaySetting(valid); err != nil {
		t.Fatalf("default setting should be valid: %v", err)
	}
	cases := map[string]upstreamMediaRelaySettingValue{
		"unknown provider": {Provider: "weird", TTL: "1h", TimeoutSeconds: 60, MaxMB: 20},
		"bad ttl":          {Provider: upstreamRelayProviderUguu, TTL: "5h", TimeoutSeconds: 60, MaxMB: 20},
		"timeout low":      {Provider: upstreamRelayProviderUguu, TTL: "1h", TimeoutSeconds: 1, MaxMB: 20},
		"timeout high":     {Provider: upstreamRelayProviderUguu, TTL: "1h", TimeoutSeconds: 9999, MaxMB: 20},
		"max too high":     {Provider: upstreamRelayProviderUguu, TTL: "1h", TimeoutSeconds: 60, MaxMB: 9999},
	}
	for name, value := range cases {
		if err := validateUpstreamMediaRelaySetting(value); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}
