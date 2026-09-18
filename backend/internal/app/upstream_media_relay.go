package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

const upstreamMediaRelaySettingKey = "upstream_media_relay"

const (
	upstreamRelayProviderLitterbox = "litterbox"
	upstreamRelayProviderUguu      = "uguu"
	upstreamRelayProviderTmpfiles  = "tmpfiles"
)

const (
	defaultUpstreamRelayMaxMB          = 20
	defaultUpstreamRelayTimeoutSeconds = 60
	minUpstreamRelayTimeoutSeconds     = 5
	maxUpstreamRelayTimeoutSeconds     = 300
	minUpstreamRelayMaxMB              = 1
	maxUpstreamRelayMaxMB              = 100
)

// 中继地址在过期前这段时间内复用，避免同一素材在连续任务里反复上传。
const upstreamRelayReuseWindow = 5 * time.Minute

// upstreamRelayProviderSpec 描述一个第三方临时图床的上传入口。
// 这些服务只提供一小时的匿名托管，因此中继仅作为本地部署的兜底通道：
// 部署自备对象存储或服务器公网地址时，参考素材走原有链路。
type upstreamRelayProviderSpec struct {
	endpoint string
	// ttl 只有 catbox 系列支持，用于指定托管时长。
	supportsTTL bool
}

var upstreamRelayProviders = map[string]upstreamRelayProviderSpec{
	upstreamRelayProviderLitterbox: {endpoint: "https://litterbox.catbox.moe/resources/internals/api.php", supportsTTL: true},
	upstreamRelayProviderUguu:      {endpoint: "https://uguu.se/upload.php"},
	upstreamRelayProviderTmpfiles:  {endpoint: "https://tmpfiles.org/api/v1/upload"},
}

var upstreamRelayTTLOptions = []string{"1h", "12h", "24h", "72h"}

type upstreamMediaRelaySettingValue struct {
	Enabled           bool     `json:"enabled"`
	Provider          string   `json:"provider"`
	FallbackProviders []string `json:"fallbackProviders"`
	TTL               string   `json:"ttl"`
	TimeoutSeconds    int      `json:"timeoutSeconds"`
	MaxMB             int      `json:"maxMB"`
}

type UpstreamMediaRelaySettingRequest struct {
	Enabled           bool     `json:"enabled"`
	Provider          string   `json:"provider"`
	FallbackProviders []string `json:"fallbackProviders"`
	TTL               string   `json:"ttl"`
	TimeoutSeconds    int      `json:"timeoutSeconds"`
	MaxMB             int      `json:"maxMB"`
}

type PublicUpstreamMediaRelaySetting struct {
	Enabled bool `json:"enabled"`
	// Providers 是实际生效的图床顺序，含兜底项；EnvDisabled 表示部署级环境变量已强制关闭中继。
	Providers   []string  `json:"providers"`
	TTL         string    `json:"ttl"`
	MaxMB       int       `json:"maxMB"`
	EnvDisabled bool      `json:"envDisabled"`
	Configured  bool      `json:"configured"`
	UpdatedBy   string    `json:"updatedBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func defaultUpstreamMediaRelaySetting() upstreamMediaRelaySettingValue {
	return upstreamMediaRelaySettingValue{
		Enabled:           true,
		Provider:          upstreamRelayProviderLitterbox,
		FallbackProviders: []string{upstreamRelayProviderUguu, upstreamRelayProviderTmpfiles},
		TTL:               "1h",
		TimeoutSeconds:    defaultUpstreamRelayTimeoutSeconds,
		MaxMB:             defaultUpstreamRelayMaxMB,
	}
}

func normalizeUpstreamMediaRelaySetting(value upstreamMediaRelaySettingValue) upstreamMediaRelaySettingValue {
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	if _, ok := upstreamRelayProviders[value.Provider]; !ok {
		value.Provider = upstreamRelayProviderLitterbox
	}
	fallbacks := make([]string, 0, len(value.FallbackProviders))
	seen := map[string]bool{value.Provider: true}
	for _, item := range value.FallbackProviders {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" || seen[item] {
			continue
		}
		if _, ok := upstreamRelayProviders[item]; !ok {
			continue
		}
		seen[item] = true
		fallbacks = append(fallbacks, item)
	}
	value.FallbackProviders = fallbacks
	value.TTL = strings.ToLower(strings.TrimSpace(value.TTL))
	if !validUpstreamRelayTTL(value.TTL) {
		value.TTL = "1h"
	}
	if value.TimeoutSeconds < minUpstreamRelayTimeoutSeconds || value.TimeoutSeconds > maxUpstreamRelayTimeoutSeconds {
		value.TimeoutSeconds = defaultUpstreamRelayTimeoutSeconds
	}
	if value.MaxMB < minUpstreamRelayMaxMB || value.MaxMB > maxUpstreamRelayMaxMB {
		value.MaxMB = defaultUpstreamRelayMaxMB
	}
	return value
}

func validUpstreamRelayTTL(value string) bool {
	for _, option := range upstreamRelayTTLOptions {
		if option == value {
			return true
		}
	}
	return false
}

// upstreamMediaRelayEnvDisabled 让商用部署在不改数据库的前提下强制关闭第三方图床中继，
// 避免把用户素材交给公共托管服务。
func upstreamMediaRelayEnvDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CANVAS_UPSTREAM_MEDIA_RELAY"))) {
	case "0", "false", "off", "no", "disabled":
		return true
	default:
		return false
	}
}

func upstreamRelayProviderChain(value upstreamMediaRelaySettingValue) []string {
	value = normalizeUpstreamMediaRelaySetting(value)
	chain := make([]string, 0, len(value.FallbackProviders)+1)
	chain = append(chain, value.Provider)
	chain = append(chain, value.FallbackProviders...)
	return chain
}

// relayProvider 读取图床规格；测试可注入本地上游，避免单元测试访问真实第三方服务。
func (s *Service) relayProvider(name string) (upstreamRelayProviderSpec, bool) {
	if s.relayProviderOverride != nil {
		if spec, ok := s.relayProviderOverride[name]; ok {
			return spec, true
		}
	}
	spec, ok := upstreamRelayProviders[name]
	return spec, ok
}

func (s *Service) readUpstreamMediaRelaySetting() (*model.SystemSetting, upstreamMediaRelaySettingValue, error) {
	setting, err := s.repo.SystemSetting(upstreamMediaRelaySettingKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, normalizeUpstreamMediaRelaySetting(defaultUpstreamMediaRelaySetting()), nil
	}
	if err != nil {
		return nil, upstreamMediaRelaySettingValue{}, err
	}
	value := defaultUpstreamMediaRelaySetting()
	if strings.TrimSpace(setting.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(setting.ValueJSON), &value); err != nil {
			return nil, upstreamMediaRelaySettingValue{}, errors.New("上游素材中继配置格式无效")
		}
	}
	return setting, normalizeUpstreamMediaRelaySetting(value), nil
}

func (s *Service) upstreamMediaRelaySetting() (upstreamMediaRelaySettingValue, error) {
	_, value, err := s.readUpstreamMediaRelaySetting()
	if err != nil {
		return upstreamMediaRelaySettingValue{}, err
	}
	if upstreamMediaRelayEnvDisabled() {
		value.Enabled = false
	}
	return value, nil
}

func (s *Service) AdminUpstreamMediaRelaySetting(actor *model.User) (*PublicUpstreamMediaRelaySetting, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	setting, value, err := s.readUpstreamMediaRelaySetting()
	if err != nil {
		return nil, err
	}
	return publicUpstreamMediaRelaySetting(setting, value, upstreamMediaRelayEnvDisabled()), nil
}

func (s *Service) UpdateUpstreamMediaRelaySetting(actor *model.User, req UpstreamMediaRelaySettingRequest) (*PublicUpstreamMediaRelaySetting, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	currentSetting, _, err := s.readUpstreamMediaRelaySetting()
	if err != nil {
		return nil, err
	}
	next := upstreamMediaRelaySettingFromRequest(req)
	if err := validateUpstreamMediaRelaySetting(next); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	setting := model.SystemSetting{
		Key:       upstreamMediaRelaySettingKey,
		ValueJSON: string(encoded),
		UpdatedBy: actor.ID,
	}
	if currentSetting != nil {
		setting.CreatedAt = currentSetting.CreatedAt
	}
	if err := s.repo.SaveSystemSetting(&setting); err != nil {
		return nil, err
	}
	if next.Enabled {
		if err := s.appendAdminAudit(actor, "upstream_media_relay.update", "system_setting", upstreamMediaRelaySettingKey, "启用上游素材图床中继", map[string]any{"enabled": true, "providers": upstreamRelayProviderChain(next)}); err != nil {
			return nil, err
		}
	} else {
		if err := s.appendAdminAudit(actor, "upstream_media_relay.update", "system_setting", upstreamMediaRelaySettingKey, "关闭上游素材图床中继", map[string]any{"enabled": false}); err != nil {
			return nil, err
		}
	}
	return publicUpstreamMediaRelaySetting(&setting, next, upstreamMediaRelayEnvDisabled()), nil
}

func upstreamMediaRelaySettingFromRequest(req UpstreamMediaRelaySettingRequest) upstreamMediaRelaySettingValue {
	defaults := defaultUpstreamMediaRelaySetting()
	value := upstreamMediaRelaySettingValue{
		Enabled:           req.Enabled,
		Provider:          req.Provider,
		FallbackProviders: req.FallbackProviders,
		TTL:               req.TTL,
		TimeoutSeconds:    req.TimeoutSeconds,
		MaxMB:             req.MaxMB,
	}
	// 只看开关的调用（例如首屏只切换 enabled）保留其余字段；完整表单始终带上 provider。
	if strings.TrimSpace(value.Provider) == "" && strings.TrimSpace(value.TTL) == "" && value.TimeoutSeconds == 0 && value.MaxMB == 0 {
		defaults.Enabled = value.Enabled
		if len(value.FallbackProviders) > 0 {
			defaults.FallbackProviders = append([]string(nil), value.FallbackProviders...)
		}
		return normalizeUpstreamMediaRelaySetting(defaults)
	}
	return normalizeUpstreamMediaRelaySetting(value)
}

func validateUpstreamMediaRelaySetting(value upstreamMediaRelaySettingValue) error {
	if strings.TrimSpace(value.Provider) == "" {
		return BadAuthRequest("请选择主用图床")
	}
	if _, ok := upstreamRelayProviders[value.Provider]; !ok {
		return BadAuthRequest("仅支持 litterbox、uguu 和 tmpfiles 三个临时图床")
	}
	for _, item := range value.FallbackProviders {
		if _, ok := upstreamRelayProviders[item]; !ok {
			return BadAuthRequest("兜底图床只能是 litterbox、uguu 或 tmpfiles")
		}
	}
	if !validUpstreamRelayTTL(value.TTL) {
		return BadAuthRequest("图床存活时长只能是 1h、12h、24h 或 72h")
	}
	if value.TimeoutSeconds < minUpstreamRelayTimeoutSeconds || value.TimeoutSeconds > maxUpstreamRelayTimeoutSeconds {
		return BadAuthRequest(fmt.Sprintf("单次上传超时必须是 %d-%d 秒", minUpstreamRelayTimeoutSeconds, maxUpstreamRelayTimeoutSeconds))
	}
	if value.MaxMB < minUpstreamRelayMaxMB || value.MaxMB > maxUpstreamRelayMaxMB {
		return BadAuthRequest(fmt.Sprintf("中继单文件上限必须是 %d-%d MB", minUpstreamRelayMaxMB, maxUpstreamRelayMaxMB))
	}
	return nil
}

func publicUpstreamMediaRelaySetting(setting *model.SystemSetting, value upstreamMediaRelaySettingValue, envDisabled bool) *PublicUpstreamMediaRelaySetting {
	result := &PublicUpstreamMediaRelaySetting{
		Enabled:     value.Enabled && !envDisabled,
		Providers:   upstreamRelayProviderChain(value),
		TTL:         value.TTL,
		MaxMB:       value.MaxMB,
		EnvDisabled: envDisabled,
	}
	if setting != nil {
		result.Configured = true
		result.UpdatedBy = setting.UpdatedBy
		result.CreatedAt = setting.CreatedAt
		result.UpdatedAt = setting.UpdatedAt
	}
	return result
}

// upstreamProviderMediaURL 为上游模型解析参考素材地址：本地存储且部署没有自备服务器
// 公网地址时先试图床中继，其余情况保持原有的短时签名地址链路。
func (s *Service) upstreamProviderMediaURL(userID string, resource *model.Resource, expiresAt time.Time) (string, error) {
	relayedURL, attempted, relayErr := s.relayedProviderMediaURL(userID, resource)
	if attempted && relayErr == nil {
		return relayedURL, nil
	}
	directURL, directErr := s.directResourceURL(resource, expiresAt)
	if directErr == nil {
		return directURL, nil
	}
	if attempted && relayErr != nil {
		// 两个原因都要给用户：签名地址说明为什么要中转，中转原因说明这素材为什么转不动。
		// 图床错误文案已在各处收敛，不含密钥或上游正文。
		appErr := NewAppError(http.StatusBadRequest, directErr.Error()+"；上游素材中转失败："+relayErr.Error())
		appErr.Cause = relayErr
		return "", appErr
	}
	return "", directErr
}

func resourceUsesLocalStorage(resource *model.Resource) bool {
	if resource == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(resource.Provider))
	return provider == "" || provider == "local"
}

func cachedRelayURL(resource *model.Resource, now time.Time) string {
	if resource == nil || strings.TrimSpace(resource.RelayURL) == "" || resource.RelayExpiresAt == nil {
		return ""
	}
	if resource.RelayExpiresAt.Before(now.Add(upstreamRelayReuseWindow)) {
		return ""
	}
	return resource.RelayURL
}

func (s *Service) relayedProviderMediaURL(userID string, resource *model.Resource) (string, bool, error) {
	if !resourceUsesLocalStorage(resource) {
		return "", false, nil
	}
	setting, err := s.upstreamMediaRelaySetting()
	if err != nil || !setting.Enabled {
		return "", false, nil
	}
	// 部署自备服务器公网地址时优先使用自身链路，不把用户素材交给第三方图床。
	if configured, err := s.configuredPublicBaseURL(); err == nil && configured != "" {
		return "", false, nil
	}
	if cached := cachedRelayURL(resource, time.Now()); cached != "" {
		return cached, true, nil
	}
	relayedURL, err := s.relayResourceMedia(userID, resource, setting)
	if err != nil {
		return "", true, err
	}
	return relayedURL, true, nil
}

func (s *Service) relayResourceMedia(userID string, resource *model.Resource, setting upstreamMediaRelaySettingValue) (string, error) {
	stream, err := s.openResourceRange(userID, resource, "")
	if err != nil {
		return "", fmt.Errorf("读取待中继素材失败：%w", err)
	}
	defer stream.Body.Close()
	limit := int64(setting.MaxMB) << 20
	data, err := io.ReadAll(io.LimitReader(stream.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("读取待中继素材失败：%w", err)
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("素材超过图床中继上限 %dMB，请改用对象存储", setting.MaxMB)
	}
	if len(data) == 0 {
		return "", errors.New("待中继素材内容为空")
	}
	fileName := relayUploadFileName(resource)
	relayedURL, err := s.performRelayUpload(setting, fileName, resource.MimeType, data)
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().Add(upstreamRelayTTL(setting))
	if err := s.repo.SaveResourceRelayURL(resource.ID, relayedURL, expiresAt); err == nil {
		resource.RelayURL = relayedURL
		resource.RelayExpiresAt = &expiresAt
	}
	return relayedURL, nil
}

// performRelayUpload 是图床上传的唯一出口，便于测试替换而不发起真实网络请求。
func (s *Service) performRelayUpload(setting upstreamMediaRelaySettingValue, fileName string, mimeType string, data []byte) (string, error) {
	if s.relayUploadOverride != nil {
		return s.relayUploadOverride(setting.Provider, fileName, mimeType, data)
	}
	return s.uploadToTemporaryImageHost(context.Background(), setting, fileName, mimeType, data)
}

func upstreamRelayTTL(setting upstreamMediaRelaySettingValue) time.Duration {
	ttl, err := time.ParseDuration(normalizeUpstreamMediaRelaySetting(setting).TTL)
	if err != nil || ttl <= 0 {
		return time.Hour
	}
	return ttl
}

func relayUploadFileName(resource *model.Resource) string {
	if resource == nil {
		return "reference.bin"
	}
	name := strings.TrimSpace(resource.ID)
	if name == "" {
		name = "reference"
	}
	extension := resourceFileExtension(resource.ObjectKey, resource.MimeType, resource.Kind)
	if extension != "" {
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		if !strings.HasSuffix(name, extension) {
			name += extension
		}
	}
	return name
}

// uploadToTemporaryImageHost 按主用图床加兜底顺序上传，全部失败才返回错误，
// 与本地素材导入的降级语义一致：单个公共图床不可用不应直接中断生成任务。
func (s *Service) uploadToTemporaryImageHost(ctx context.Context, setting upstreamMediaRelaySettingValue, fileName string, mimeType string, data []byte) (string, error) {
	chain := upstreamRelayProviderChain(setting)
	failures := make([]string, 0, len(chain))
	for _, name := range chain {
		spec, ok := s.relayProvider(name)
		if !ok {
			failures = append(failures, name+"：不支持的临时图床")
			continue
		}
		relayedURL, err := s.uploadToTemporaryImageHostProvider(ctx, name, spec, setting, fileName, mimeType, data)
		if err == nil {
			return relayedURL, nil
		}
		failures = append(failures, name+"："+err.Error())
	}
	return "", fmt.Errorf("素材图床中继失败（%s）", strings.Join(failures, "；"))
}

func (s *Service) uploadToTemporaryImageHostProvider(ctx context.Context, name string, spec upstreamRelayProviderSpec, setting upstreamMediaRelaySettingValue, fileName string, mimeType string, data []byte) (string, error) {
	endpoint, err := ValidateOutboundURL(spec.endpoint)
	if err != nil {
		return "", err
	}
	body, contentType, err := buildUpstreamRelayUpload(name, spec, setting, fileName, mimeType, data)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", contentType)
	ApplyDefaultOutboundHeaders(request)
	client := OutboundHTTPClient(time.Duration(setting.TimeoutSeconds) * time.Second)
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("上传超时")
		}
		return "", errors.New("上传请求失败")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", errors.New("上传响应读取失败")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("上传被拒绝（HTTP %d）", response.StatusCode)
	}
	return parseUpstreamRelayResponse(name, payload)
}

func buildUpstreamRelayUpload(name string, spec upstreamRelayProviderSpec, setting upstreamMediaRelaySettingValue, fileName string, mimeType string, data []byte) (io.Reader, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	writeFile := func(field string) error {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, escapeMultipartValue(fileName)))
		resolved := strings.TrimSpace(mimeType)
		if resolved == "" {
			resolved = "application/octet-stream"
		}
		header.Set("Content-Type", resolved)
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		_, err = part.Write(data)
		return err
	}
	switch name {
	case upstreamRelayProviderLitterbox:
		_ = writer.WriteField("reqtype", "fileupload")
		if spec.supportsTTL {
			_ = writer.WriteField("time", normalizeUpstreamMediaRelaySetting(setting).TTL)
		}
		if err := writeFile("fileToUpload"); err != nil {
			return nil, "", err
		}
	case upstreamRelayProviderUguu:
		if err := writeFile("files[]"); err != nil {
			return nil, "", err
		}
	case upstreamRelayProviderTmpfiles:
		if err := writeFile("file"); err != nil {
			return nil, "", err
		}
	default:
		return nil, "", errors.New("不支持的临时图床")
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &buffer, writer.FormDataContentType(), nil
}

func escapeMultipartValue(value string) string {
	return strings.NewReplacer("\\", "_", `"`, "_", "\r", "_", "\n", "_").Replace(value)
}

func parseUpstreamRelayResponse(name string, payload []byte) (string, error) {
	switch name {
	case upstreamRelayProviderLitterbox:
		return normalizeRelayedURL(strings.TrimSpace(string(payload)))
	case upstreamRelayProviderUguu:
		var decoded struct {
			Success bool `json:"success"`
			Files   []struct {
				URL string `json:"url"`
			} `json:"files"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return "", errors.New("上传响应格式无效")
		}
		if !decoded.Success || len(decoded.Files) == 0 {
			return "", errors.New("上传响应无效")
		}
		return normalizeRelayedURL(decoded.Files[0].URL)
	case upstreamRelayProviderTmpfiles:
		var decoded struct {
			Status string `json:"status"`
			Data   struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return "", errors.New("上传响应格式无效")
		}
		if decoded.Status != "success" {
			return "", errors.New("上传响应无效")
		}
		return normalizeTmpfilesDirectURL(decoded.Data.URL)
	default:
		return "", errors.New("不支持的临时图床")
	}
}

// normalizeTmpfilesDirectURL 把 tmpfiles 的网页地址换成直链，否则上游模型拿到的是 HTML 页面。
func normalizeTmpfilesDirectURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("上传响应无效")
	}
	if parsed.Hostname() == "tmpfiles.org" && !strings.HasPrefix(parsed.Path, "/dl/") {
		parsed.Path = "/dl" + parsed.Path
	}
	return normalizeRelayedURL(parsed.String())
}

func normalizeRelayedURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("上传响应无效")
	}
	// 上游模型必须能直接读取中继地址，因此只接受可公网访问的 HTTPS 链接。
	parsed, err := ValidateOutboundURL(value)
	if err != nil {
		return "", errors.New("上传响应不是可公网访问的 HTTPS 地址")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("上传响应不是 HTTPS 地址")
	}
	return parsed.String(), nil
}
