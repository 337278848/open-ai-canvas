package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/assets"
	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"
)

const providerResourceURLTTL = 4 * time.Hour

// 直链只用于浏览器/渠道即时下载，保持短时有效，避免把长期可用的对象地址落到前端。
const directResourceURLTTL = 5 * time.Minute

var errInvalidGeneratedDataURL = errors.New("生成内容 data URL 无效")

type ResourceStream = assets.ResourceStream

type ResourceDelivery = assets.ResourceDelivery

func (s *Service) Resources(userID string, limit int) ([]model.Resource, error) {
	resources, err := s.repo.Resources(userID, limit)
	for index := range resources {
		resources[index].PublicURL = ""
	}
	return resources, err
}

func (s *Service) Resource(userID string, id string) (*model.Resource, error) {
	resource, err := s.repo.ResourceForUser(userID, id)
	if resource != nil {
		resource.PublicURL = ""
	}
	return resource, err
}

// DirectResourceURL 先校验资源归属，再按实际存储位置签发短时下载地址。
// 该地址的用途是交给上游模型访问，因此在本地存储且部署没有自备公网地址时，
// 与 provider 链路一致地优先换取图床中继地址，浏览器侧的视频渠道同样依赖它。
func (s *Service) DirectResourceURL(userID string, id string) (string, error) {
	resource, err := s.repo.ResourceForUser(userID, id)
	if err != nil {
		return "", err
	}
	return s.upstreamProviderMediaURL(userID, resource, time.Now().Add(directResourceURLTTL))
}

func (s *Service) directResourceURL(resource *model.Resource, expiresAt time.Time) (string, error) {
	if resource == nil {
		return "", errors.New("资源不存在")
	}
	if resource.Status != model.ResourceStatusReady {
		return "", BadAuthRequest("资源尚未上传完成")
	}
	if resource.Provider == "local" {
		return s.signedPublicResourceURL(resource, expiresAt)
	}
	setting, err := s.ossSettingForResource(resource.UserID, resource)
	if err != nil {
		return "", err
	}
	setting.Provider = firstNonEmpty(resource.Provider, setting.Provider)
	setting.Endpoint = firstNonEmpty(resource.Endpoint, setting.Endpoint)
	setting.Bucket = firstNonEmpty(resource.Bucket, setting.Bucket)
	if setting.Provider == s3Provider && !publicHTTPSStorageEndpoint(setting.Endpoint) {
		return s.signedHTTPSPublicResourceURL(resource, expiresAt)
	}
	return signedOSSObjectURL(setting, resource.ObjectKey, expiresAt)
}

func (s *Service) signedPublicResourceURL(resource *model.Resource, expiresAt time.Time) (string, error) {
	if resource == nil {
		return "", errors.New("资源不存在")
	}
	baseURL, err := s.publicResourceBaseURL()
	if err != nil {
		return "", err
	}
	expires := strconv.FormatInt(expiresAt.UTC().Unix(), 10)
	signature, err := s.signPublicResource(resource.ID, expires)
	if err != nil {
		return "", err
	}
	ext := resourceFileExtension(resource.ObjectKey, resource.MimeType, resource.Kind)
	filename := resource.ID
	if ext != "" {
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		filename += ext
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/api/public/resources/" + url.PathEscape(resource.ID) + "/file/" + url.PathEscape(filename)
	query := baseURL.Query()
	query.Set("expires", expires)
	query.Set("signature", signature)
	baseURL.RawQuery = query.Encode()
	return baseURL.String(), nil
}

func (s *Service) signedHTTPSPublicResourceURL(resource *model.Resource, expiresAt time.Time) (string, error) {
	baseURL, err := s.publicResourceBaseURL()
	if err != nil {
		return "", err
	}
	if baseURL.Scheme != "https" {
		return "", BadAuthRequest("私网或 HTTP S3 用于上游资源时，服务器公开访问地址必须使用 HTTPS")
	}
	return s.signedPublicResourceURL(resource, expiresAt)
}

func (s *Service) verifyPublicResourceSignature(resourceID string, expires string, signature string) error {
	if strings.TrimSpace(signature) == "" || !decimalDigits(expires) {
		return Forbidden("匿名下载链接无效")
	}
	expiresAt, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || time.Now().UTC().Unix() > expiresAt {
		return Forbidden("匿名下载链接已过期")
	}
	expected, err := s.signPublicResource(resourceID, expires)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return Forbidden("匿名下载链接无效")
	}
	return nil
}

func (s *Service) signPublicResource(resourceID string, expires string) (string, error) {
	key, err := s.settingsEncryptionKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(resourceID + "\n" + expires))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) publicResourceBaseURL() (*url.URL, error) {
	raw, err := s.configuredPublicBaseURL()
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, BadAuthRequest("服务器本地存储尚未配置服务器访问地址，请设置 CANVAS_PUBLIC_BASE_URL 或在存储设置中配置公网访问地址（或改用 OSS 存储）")
	}
	return validatePublicResourceBaseURL(raw)
}

// configuredPublicBaseURL 只读取部署自备的服务器访问地址，不做 DNS 与协议校验。
// 上游素材是否需要图床中继只取决于「有没有配置」，因此这条判定不能依赖真实出网解析。
func (s *Service) configuredPublicBaseURL() (string, error) {
	if raw := strings.TrimSpace(s.publicBaseURLOverride); raw != "" {
		return raw, nil
	}
	_, setting, err := s.readOSSSetting()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(firstNonEmpty(setting.PublicBaseURL, os.Getenv("CANVAS_PUBLIC_BASE_URL"))), nil
}

func validatePublicResourceBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return nil, BadAuthRequest("服务器访问地址必须是完整的 http/https 地址")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, BadAuthRequest("服务器访问地址只支持 http/https")
	}
	if parsed.User != nil {
		return nil, BadAuthRequest("服务器访问地址不能包含认证信息")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, BadAuthRequest("服务器访问地址不能包含查询参数或片段")
	}
	if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/api") {
		return nil, BadAuthRequest("服务器访问地址请填写根地址，不要包含 /api")
	}
	return parsed, nil
}

func normalizeResourceKind(kind string, mimeType string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "image", "video", "audio", "file":
		return kind
	}
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if strings.HasPrefix(mimeType, "video/") {
		return "video"
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return "audio"
	}
	return "file"
}

func ossObjectKey(setting ossSettingValue, userID string, kind string, fileName string, mimeType string, now time.Time) string {
	ext := resourceFileExtension(fileName, mimeType, kind)
	name := newID()
	parts := []string{setting.PathPrefix, "users", safeObjectSegment(userID), kind, now.Format("2006/01/02"), name + ext}
	return strings.Trim(strings.Join(nonEmptySegments(parts), "/"), "/")
}

func normalizeSingleByteRange(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 || !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return ""
	}
	start, end, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok || (start == "" && end == "") || !decimalDigits(start) || !decimalDigits(end) {
		return ""
	}
	return "bytes=" + start + "-" + end
}

func decimalDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func safeObjectSegment(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	return strings.Trim(value, "-")
}

func firstNonEmpty(values ...string) string {
	return kernel.FirstNonEmpty(values...)
}

func nonEmptySegments(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Trim(strings.TrimSpace(path.Clean("/"+value)), "/")
		if value != "" && value != "." {
			result = append(result, value)
		}
	}
	return result
}
