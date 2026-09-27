package hostupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

const defaultDeploymentImageRepository = "ghcr.io/337278848/open-ai-canvas"
const legacyDeploymentImageRepository = "ghcr.io/ddcat-ai/open-ai-canvas"

func isLegacyReleaseVersion(version string) bool {
	switch strings.TrimPrefix(strings.TrimSpace(version), "v") {
	case "1.2.9", "1.5.7", "1.5.7.1":
		return true
	default:
		return false
	}
}

func (m *Manager) latestRelease(ctx context.Context) (*Release, error) {
	url := "https://api.github.com/repos/" + m.config.Repository + "/releases?per_page=30"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "open-ai-canvas-host-updater")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if m.config.GitHubToken != "" {
		request.Header.Set("Authorization", "Bearer "+m.config.GitHubToken)
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("请求版本服务：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("版本服务返回 HTTP %d", response.StatusCode)
	}
	var releases []githubRelease
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxReleaseResponseBytes))
	if err := decoder.Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析版本信息：%w", err)
	}
	filtered := make([]githubRelease, 0, len(releases))
	for _, release := range releases {
		if !release.Draft && strings.HasPrefix(release.TagName, "v") {
			filtered = append(filtered, release)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("暂未发布可用版本")
	}
	sort.SliceStable(filtered, func(i, j int) bool { return CompareVersions(filtered[i].TagName, filtered[j].TagName) > 0 })
	latest := filtered[0]
	return &Release{Version: latest.TagName, Name: latest.Name, Body: sanitizeReleaseBody(latest.Body), URL: latest.HTMLURL, PublishedAt: latest.PublishedAt, Prerelease: latest.Prerelease}, nil
}

// sanitizeReleaseBody keeps only product-facing feature notes. Remote release
// text is not trusted because the update source may contain engineering,
// repository or contributor metadata that must not reach the admin UI.
func sanitizeReleaseBody(source string) string {
	forbidden := []string{
		"pr #", "原作者", "作者：", "贡献者", "主分支", "上游", "fork",
		"github", "ghcr", "开源", "repository", "仓库", "host updater",
	}
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		blocked := false
		for _, pattern := range forbidden {
			if strings.Contains(lower, strings.ToLower(pattern)) {
				blocked = true
				break
			}
		}
		if !blocked {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func (m *Manager) currentVersion() (string, error) {
	values, err := readEnvFile(m.envPath())
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(values["CANVAS_IMAGE_TAG"])
	if version == "" || version == "latest" {
		return "", errors.New("CANVAS_IMAGE_TAG 必须固定为发布版本，不能使用 latest")
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version, nil
}

func (m *Manager) prepareTargetCompose(targetVersion string) (string, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", m.config.Repository, targetVersion, m.config.ComposeFile)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("下载目标 Compose：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载目标 Compose 返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("目标 Compose 文件为空")
	}
	imageRepository, err := m.imageRepository()
	if err != nil {
		return "", err
	}
	data, err = normalizeComposeImageContractForRepository(data, imageRepository)
	if err != nil {
		return "", fmt.Errorf("目标 Compose 不兼容：%w", err)
	}
	path := filepath.Join(m.config.StateDir, "compose-"+strings.TrimPrefix(targetVersion, "v")+".next.yml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("保存目标 Compose：%w", err)
	}
	return path, nil
}

func (m *Manager) preflight(composePath, targetVersion string) error {
	if _, err := os.Stat(m.composePath()); err != nil {
		return fmt.Errorf("读取当前 Compose：%w", err)
	}
	if _, err := os.Stat(m.envPath()); err != nil {
		return fmt.Errorf("读取部署环境：%w", err)
	}
	imageRepository, err := m.imageRepository()
	if err != nil {
		return err
	}
	if err := normalizeComposeImageFile(composePath, "目标", imageRepository); err != nil {
		return err
	}
	current, err := m.currentVersion()
	if err != nil {
		return err
	}
	if _, _, err := m.currentDeploymentContract(current, imageRepository); err != nil {
		return err
	}
	if m.config.SelfUpdate {
		if strings.TrimSpace(m.config.BinaryPath) == "" {
			return errors.New("检查在线更新服务安装目录：二进制路径为空")
		}
		if err := checkWritableDirectory(filepath.Dir(m.config.BinaryPath)); err != nil {
			return fmt.Errorf("检查在线更新服务安装目录：%w", err)
		}
	}
	if err := checkBackupDiskSpace(m.config.BackupDir); err != nil {
		return err
	}
	targetImages, err := m.immutableImageRefs(targetVersion)
	if err != nil {
		return err
	}
	if err := m.composeWithImages(composePath, targetVersion, targetImages, 2*time.Minute, nil, "config", "--quiet"); err != nil {
		return fmt.Errorf("目标 Compose 校验失败：%w", err)
	}
	if err := m.checkHealthOnce(m.healthURL(), current); err != nil {
		return fmt.Errorf("当前运行版本与部署配置不一致或服务未就绪：%w", err)
	}
	return nil
}

func validateComposeImageContract(data []byte) error {
	return validateComposeImageContractForRepository(data, defaultDeploymentImageRepository)
}

func validateComposeImageContractForRepository(data []byte, imageRepository string) error {
	_, err := processComposeImageContract(data, imageRepository, false)
	return err
}

func normalizeComposeImageContract(data []byte) ([]byte, error) {
	return normalizeComposeImageContractForRepository(data, defaultDeploymentImageRepository)
}

func normalizeComposeImageContractForRepository(data []byte, imageRepository string) ([]byte, error) {
	normalized, err := processComposeImageContract(data, imageRepository, true)
	if err != nil {
		return nil, err
	}
	if err := validateComposeImageContractForRepository(normalized, imageRepository); err != nil {
		return nil, err
	}
	return normalized, nil
}

func processComposeImageContract(data []byte, imageRepository string, convertLegacy bool) ([]byte, error) {
	required := map[string]string{
		"backend": "CANVAS_BACKEND_IMAGE",
		"migrate": "CANVAS_BACKEND_IMAGE",
		"web":     "CANVAS_WEB_IMAGE",
	}
	components := map[string]string{
		"backend": "backend",
		"migrate": "backend",
		"web":     "web",
	}
	found := make(map[string]bool, len(required))
	servicesIndent := -1
	currentService := ""
	currentServiceIndent := -1
	lines := strings.Split(string(data), "\n")
	for index, rawLine := range lines {
		line := strings.TrimSuffix(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if servicesIndent < 0 {
			if trimmed == "services:" {
				servicesIndent = indent
			}
			continue
		}
		if indent <= servicesIndent {
			currentService = ""
			currentServiceIndent = -1
			continue
		}
		if currentService != "" && indent <= currentServiceIndent {
			currentService = ""
			currentServiceIndent = -1
		}
		if currentService == "" && indent == servicesIndent+2 {
			serviceName, ok := composeServiceName(trimmed)
			if !ok {
				continue
			}
			currentService = serviceName
			currentServiceIndent = indent
			continue
		}
		if currentService == "" || indent != currentServiceIndent+2 || !strings.HasPrefix(trimmed, "image:") {
			continue
		}
		variable, ok := required[currentService]
		if !ok {
			continue
		}
		value, ok := composeImageScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "image:")))
		if !ok {
			return nil, fmt.Errorf("services.%s 的 image 字段不是支持的标量", currentService)
		}
		if usesComposeImageVariable(value, variable) {
			found[currentService] = true
			continue
		}
		if convertLegacy && isLegacyComposeImage(value, imageRepository, components[currentService]) {
			indentPrefix := rawLine[:len(rawLine)-len(strings.TrimLeft(rawLine, " \t"))]
			lineEnding := ""
			if strings.HasSuffix(rawLine, "\r") {
				lineEnding = "\r"
			}
			lines[index] = indentPrefix + "image: ${" + variable + ":?请先配置 " + variable + "}" + lineEnding
			found[currentService] = true
			continue
		}
		return nil, fmt.Errorf("services.%s 的 image 字段必须使用 ${%s} 镜像变量", currentService, variable)
	}
	for _, service := range []string{"backend", "migrate", "web"} {
		variable := required[service]
		if !found[service] {
			return nil, fmt.Errorf("services.%s 的 image 字段必须使用 ${%s} 镜像变量", service, variable)
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func validateComposeImageFile(path, label string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取%s Compose：%w", label, err)
	}
	if err := validateComposeImageContract(data); err != nil {
		return fmt.Errorf("%s Compose 不兼容：%w", label, err)
	}
	return nil
}

func normalizeComposeImageFile(path, label, imageRepository string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取%s Compose：%w", label, err)
	}
	normalized, err := normalizeComposeImageContractForRepository(data, imageRepository)
	if err != nil {
		return fmt.Errorf("%s Compose 不兼容：%w", label, err)
	}
	if bytes.Equal(data, normalized) {
		return nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取%s Compose 属性：%w", label, err)
	}
	if err := writeAtomicFile(path, normalized, stat.Mode().Perm()); err != nil {
		return fmt.Errorf("写入兼容的%s Compose：%w", label, err)
	}
	return nil
}

func (m *Manager) currentDeploymentContract(version, imageRepository string) ([]byte, deploymentImages, error) {
	data, err := os.ReadFile(m.composePath())
	if err != nil {
		return nil, deploymentImages{}, fmt.Errorf("读取当前 Compose：%w", err)
	}
	normalized, err := normalizeComposeImageContractForRepository(data, imageRepository)
	if err != nil {
		return nil, deploymentImages{}, fmt.Errorf("当前 Compose 不兼容：%w", err)
	}
	values, err := readEnvFile(m.envPath())
	if err != nil {
		return nil, deploymentImages{}, err
	}
	images := deploymentImages{
		backend: values["CANVAS_BACKEND_IMAGE"],
		web:     values["CANVAS_WEB_IMAGE"],
	}
	if err := validateDeploymentImageRefs(images, imageRepository); err != nil {
		targetImages, resolveErr := m.immutableImageRefs(version)
		if resolveErr != nil {
			return nil, deploymentImages{}, resolveErr
		}
		images, err = m.resolveImageDigests(targetImages)
		if err != nil {
			return nil, deploymentImages{}, fmt.Errorf("当前部署镜像未固定为 digest，且无法从本地镜像安全解析：%w", err)
		}
	}
	if err := validateDeploymentImageRefs(images, imageRepository); err != nil {
		return nil, deploymentImages{}, err
	}
	return normalized, images, nil
}

func (m *Manager) stageCurrentDeploymentContract(normalized []byte, version string, images deploymentImages) (func() error, error) {
	composePath := m.composePath()
	envPath := m.envPath()
	originalCompose, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}
	originalEnv, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
	}
	composeStat, err := os.Stat(composePath)
	if err != nil {
		return nil, err
	}
	envStat, err := os.Stat(envPath)
	if err != nil {
		return nil, err
	}
	if err := setDeploymentImages(envPath, version, images); err != nil {
		return nil, fmt.Errorf("固定当前部署镜像 digest：%w", err)
	}
	if err := writeAtomicFile(composePath, normalized, composeStat.Mode().Perm()); err != nil {
		_ = writeAtomicFile(envPath, originalEnv, envStat.Mode().Perm())
		return nil, fmt.Errorf("写入兼容的当前 Compose：%w", err)
	}
	restore := func() error {
		if err := writeAtomicFile(envPath, originalEnv, envStat.Mode().Perm()); err != nil {
			return err
		}
		return writeAtomicFile(composePath, originalCompose, composeStat.Mode().Perm())
	}
	return restore, nil
}

func composeServiceName(trimmed string) (string, bool) {
	key, rest, ok := strings.Cut(trimmed, ":")
	if !ok || strings.ContainsAny(key, " \t") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	return key, true
}

func composeImageScalar(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if raw[0] == '"' || raw[0] == '\'' {
		quote := raw[0]
		end := strings.IndexByte(raw[1:], quote)
		if end < 0 {
			return "", false
		}
		value := raw[1 : end+1]
		remainder := strings.TrimSpace(raw[end+2:])
		if remainder != "" && !strings.HasPrefix(remainder, "#") {
			return "", false
		}
		return value, true
	}
	if comment := strings.Index(raw, " #"); comment >= 0 {
		raw = raw[:comment]
	}
	raw = strings.TrimSpace(raw)
	return raw, raw != ""
}

func usesComposeImageVariable(value, variable string) bool {
	if value == "${"+variable+"}" {
		return true
	}
	prefix := "${" + variable + ":?"
	return strings.HasPrefix(value, prefix) && strings.HasSuffix(value, "}") && len(value) > len(prefix)+1
}

func isLegacyComposeImage(value, imageRepository, component string) bool {
	for _, repository := range []string{imageRepository, defaultDeploymentImageRepository, legacyDeploymentImageRepository} {
		prefix := repository + "-" + component + ":"
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		switch strings.TrimPrefix(value, prefix) {
		case "${CANVAS_IMAGE_TAG}", "${CANVAS_IMAGE_TAG:-latest}":
			return true
		}
	}
	return false
}

func deploymentImageRepository(repository string) (string, error) {
	value := strings.TrimSpace(repository)
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !validRepositoryPart(parts[0]) || !validRepositoryPart(parts[1]) {
		return "", fmt.Errorf("不支持的部署仓库 %q，必须是安全的 owner/repository", repository)
	}
	return "ghcr.io/" + value, nil
}

func validateImageRepositoryPath(repository string) (string, error) {
	value := strings.TrimSpace(repository)
	parts := strings.Split(value, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("不支持的镜像仓库 %q，必须包含 registry 与 repository", repository)
	}
	registry := parts[0]
	if colon := strings.LastIndexByte(registry, ':'); colon >= 0 {
		if strings.Count(registry, ":") != 1 || colon == 0 || colon == len(registry)-1 {
			return "", fmt.Errorf("不支持的镜像仓库 %q", repository)
		}
		for _, char := range registry[colon+1:] {
			if char < '0' || char > '9' {
				return "", fmt.Errorf("不支持的镜像仓库 %q", repository)
			}
		}
		registry = registry[:colon]
	}
	if !validRepositoryPart(registry) {
		return "", fmt.Errorf("不支持的镜像仓库 %q", repository)
	}
	for _, part := range parts[1:] {
		if !validRepositoryPart(part) {
			return "", fmt.Errorf("不支持的镜像仓库 %q", repository)
		}
	}
	return value, nil
}

func (m *Manager) imageRepository() (string, error) {
	if strings.TrimSpace(m.config.ImageRepository) != "" {
		return validateImageRepositoryPath(m.config.ImageRepository)
	}
	return deploymentImageRepository(m.config.Repository)
}

func validRepositoryPart(value string) bool {
	if value == "" || value == "." || value == ".." || strings.Contains(value, "..") {
		return false
	}
	for index, char := range value {
		if index == 0 && ((char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9')) {
			return false
		}
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func validateDeploymentImageValues(values map[string]string, imageRepository string) error {
	for _, component := range []string{"backend", "web"} {
		key := "CANVAS_" + strings.ToUpper(component) + "_IMAGE"
		if err := validateDeploymentImageReference(values[key], imageRepository, component); err != nil {
			return fmt.Errorf("%s：%w", key, err)
		}
	}
	return nil
}

func validateDeploymentImageReference(value, imageRepository, component string) error {
	prefix := imageRepository + "-" + component + "@sha256:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("必须固定到 %s<64位十六进制摘要>", prefix)
	}
	digest := strings.TrimPrefix(value, prefix)
	if len(digest) != sha256.Size*2 || !isLowerHex(digest) {
		return fmt.Errorf("必须固定到 %s<64位十六进制摘要>", prefix)
	}
	return nil
}

func validateDeploymentImageRefs(images deploymentImages, imageRepository string) error {
	if err := validateDeploymentImageReference(images.backend, imageRepository, "backend"); err != nil {
		return err
	}
	return validateDeploymentImageReference(images.web, imageRepository, "web")
}

func validateTaggedImageRefs(images deploymentImages, imageRepository string) error {
	for _, item := range []struct {
		value     string
		component string
	}{
		{images.backend, "backend"},
		{images.web, "web"},
	} {
		prefix := imageRepository + "-" + item.component + ":"
		tag := strings.TrimPrefix(item.value, prefix)
		if !strings.HasPrefix(item.value, prefix) || !isValidImageTag(tag) {
			return fmt.Errorf("目标 %s 镜像引用不是固定 Release tag", item.component)
		}
	}
	return nil
}

func isValidImageTag(value string) bool {
	if value == "" || value == "latest" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if index == 0 {
			if !isImageTagStart(char) {
				return false
			}
			continue
		}
		if !isImageTagChar(char) {
			return false
		}
	}
	return true
}

func isImageTagStart(char rune) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') || char == '_'
}

func isImageTagChar(char rune) bool {
	return isImageTagStart(char) || char == '.' || char == '-'
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".deployment-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func checkWritableDirectory(directory string) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("在线更新服务二进制路径为空")
	}
	temporary, err := os.CreateTemp(directory, ".updater-write-test-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	if removeErr := os.Remove(name); removeErr != nil {
		return removeErr
	}
	return nil
}

func (m *Manager) prepareUpdaterBinary(targetVersion string) (string, error) {
	if !m.config.SelfUpdate {
		return "", nil
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return "", fmt.Errorf("在线更新服务自更新不支持 %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if strings.TrimSpace(m.config.BinaryPath) == "" {
		return "", errors.New("未配置在线更新服务二进制路径")
	}
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/", m.config.Repository, targetVersion)
	asset := "open-ai-canvas-host-updater-linux-" + runtime.GOARCH
	checksums, err := m.downloadReleaseAsset(baseURL+"SHA256SUMS", 1<<20)
	if err != nil {
		return "", fmt.Errorf("下载在线更新服务校验清单：%w", err)
	}
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			expected = fields[0]
			break
		}
	}
	if len(expected) != 64 {
		return "", fmt.Errorf("目标 Release 的 SHA256SUMS 缺少 %s", asset)
	}
	binary, err := m.downloadReleaseAsset(baseURL+asset, 128<<20)
	if err != nil {
		return "", fmt.Errorf("下载目标在线更新服务：%w", err)
	}
	hash := sha256.Sum256(binary)
	actual := hex.EncodeToString(hash[:])
	if actual != expected {
		return "", fmt.Errorf("在线更新服务校验失败：期望 %s，实际 %s", expected, actual)
	}
	path := filepath.Join(m.config.StateDir, asset+"-"+strings.TrimPrefix(targetVersion, "v")+".next")
	if err := os.WriteFile(path, binary, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Manager) downloadReleaseAsset(url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "open-ai-canvas-host-updater")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	reader := io.LimitReader(response.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("Release 资产超过允许大小")
	}
	return data, nil
}

func (m *Manager) restartSelf() {
	time.Sleep(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = m.runner.Run(ctx, "systemctl", []string{"restart", m.config.ServiceName}, nil, io.Discard, io.Discard)
}

type deploymentImages struct {
	backend string
	web     string
}

func immutableImageRefs(repository, version string) deploymentImages {
	imageRepository := "ghcr.io/" + strings.TrimSpace(repository)
	if validated, err := deploymentImageRepository(repository); err == nil {
		imageRepository = validated
	}
	tag := strings.TrimPrefix(version, "v")
	return deploymentImages{
		backend: imageRepository + "-backend:" + tag,
		web:     imageRepository + "-web:" + tag,
	}
}

func (m *Manager) immutableImageRefs(version string) (deploymentImages, error) {
	imageRepository, err := m.imageRepository()
	if err != nil {
		return deploymentImages{}, err
	}
	tag := strings.TrimPrefix(version, "v")
	return deploymentImages{
		backend: imageRepository + "-backend:" + tag,
		web:     imageRepository + "-web:" + tag,
	}, nil
}

func (m *Manager) compose(composePath, imageTag string, timeout time.Duration, stdout io.Writer, arguments ...string) error {
	values, err := readEnvFile(m.envPath())
	if err != nil {
		return fmt.Errorf("读取部署镜像：%w", err)
	}
	imageRepository, err := m.imageRepository()
	if err != nil {
		return err
	}
	if err := validateDeploymentImageValues(values, imageRepository); err != nil {
		return fmt.Errorf("当前部署镜像未通过 digest 预检：%w", err)
	}
	return m.composeWithImages(composePath, imageTag, deploymentImages{
		backend: values["CANVAS_BACKEND_IMAGE"],
		web:     values["CANVAS_WEB_IMAGE"],
	}, timeout, stdout, arguments...)
}

func (m *Manager) composeWithImages(composePath, imageTag string, images deploymentImages, timeout time.Duration, stdout io.Writer, arguments ...string) error {
	if images.backend == "" || images.web == "" {
		return errors.New("Compose 执行缺少 backend/web 镜像引用")
	}
	imageRepository, err := m.imageRepository()
	if err != nil {
		return err
	}
	if len(arguments) == 0 {
		return errors.New("Compose 执行缺少操作")
	}
	switch arguments[0] {
	case "config", "pull":
		if err := validateTaggedImageRefs(images, imageRepository); err != nil {
			if err := validateDeploymentImageRefs(images, imageRepository); err != nil {
				return fmt.Errorf("目标镜像引用不安全：%w", err)
			}
		}
	default:
		if err := validateDeploymentImageRefs(images, imageRepository); err != nil {
			return fmt.Errorf("运行镜像引用不安全：%w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"compose", "--env-file", m.envPath(), "-f", composePath}
	args = append(args, arguments...)
	var stderr bytes.Buffer
	if stdout == nil {
		stdout = io.Discard
	}
	environment := []string{"CANVAS_IMAGE_TAG=" + strings.TrimPrefix(imageTag, "v")}
	if images.backend != "" {
		environment = append(environment, "CANVAS_BACKEND_IMAGE="+images.backend, "CANVAS_WEB_IMAGE="+images.web)
	}
	err = m.runner.Run(ctx, "docker", args, environment, stdout, &stderr)
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 1000 {
			message = message[len(message)-1000:]
		}
		if message != "" {
			return fmt.Errorf("docker compose %s：%s", strings.Join(arguments, " "), message)
		}
		return fmt.Errorf("docker compose %s：%w", strings.Join(arguments, " "), err)
	}
	return nil
}

func (m *Manager) verifyImages(targetVersion string) (deploymentImages, error) {
	images, err := m.immutableImageRefs(targetVersion)
	if err != nil {
		return deploymentImages{}, err
	}
	return m.resolveImageDigests(images)
}

func (m *Manager) resolveImageDigests(images deploymentImages) (deploymentImages, error) {
	imageRepository, err := m.imageRepository()
	if err != nil {
		return deploymentImages{}, err
	}
	refs := []string{images.backend, images.web}
	digests := make([]string, 0, len(refs))
	for _, image := range refs {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		var output bytes.Buffer
		var stderr bytes.Buffer
		err := m.runner.Run(ctx, "docker", []string{"image", "inspect", image, "--format", "{{json .RepoDigests}}"}, nil, &output, &stderr)
		cancel()
		if err != nil {
			return deploymentImages{}, fmt.Errorf("校验目标镜像摘要失败：%s", strings.TrimSpace(stderr.String()))
		}
		var repositoryDigests []string
		if err := json.Unmarshal(output.Bytes(), &repositoryDigests); err != nil {
			return deploymentImages{}, fmt.Errorf("解析目标镜像摘要失败：%w", err)
		}
		repository := strings.Split(image, ":")[0]
		var digest string
		for _, candidate := range repositoryDigests {
			if strings.HasPrefix(candidate, repository+"@sha256:") {
				digest = candidate
				break
			}
		}
		if digest == "" {
			return deploymentImages{}, fmt.Errorf("目标镜像 %s 未包含仓库摘要", image)
		}
		component := "backend"
		if strings.HasSuffix(strings.SplitN(image, ":", 2)[0], "-web") {
			component = "web"
		}
		if err := validateDeploymentImageReference(digest, imageRepository, component); err != nil {
			return deploymentImages{}, err
		}
		digests = append(digests, digest)
	}
	return deploymentImages{backend: digests[0], web: digests[1]}, nil
}

func setDeploymentImages(path, version string, images deploymentImages) error {
	return setEnvValues(path,
		envUpdate{key: "CANVAS_IMAGE_TAG", value: strings.TrimPrefix(version, "v")},
		envUpdate{key: "CANVAS_BACKEND_IMAGE", value: images.backend},
		envUpdate{key: "CANVAS_WEB_IMAGE", value: images.web},
	)
}

func (m *Manager) createBackup(version string) (Backup, error) {
	now := time.Now().UTC()
	id := "backup-" + now.Format("20060102-150405")
	path := filepath.Join(m.config.BackupDir, id+".zip")
	temporary := path + ".partial"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Backup{}, fmt.Errorf("创建备份文件：%w", err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	hasher := sha256.New()
	archive := zip.NewWriter(io.MultiWriter(file, hasher))
	metadata, _ := json.MarshalIndent(map[string]any{"id": id, "version": version, "createdAt": now, "format": 1}, "", "  ")
	if err := writeZipBytes(archive, "metadata.json", metadata); err != nil {
		return Backup{}, err
	}
	databaseEntry, err := archive.CreateHeader(&zip.FileHeader{Name: "database.dump", Method: zip.Store})
	if err != nil {
		return Backup{}, err
	}
	values, err := readEnvFile(m.envPath())
	if err != nil {
		return Backup{}, err
	}
	postgresUser := firstNonEmpty(values["POSTGRES_USER"], "open_ai_canvas")
	postgresDB := firstNonEmpty(values["POSTGRES_DB"], "open_ai_canvas")
	if err := m.compose(m.composePath(), version, m.config.StepTimeout, databaseEntry, "exec", "-T", "postgres", "pg_dump", "-U", postgresUser, "-d", postgresDB, "-Fc"); err != nil {
		return Backup{}, fmt.Errorf("备份 PostgreSQL：%w", err)
	}
	dataEntry, err := archive.CreateHeader(&zip.FileHeader{Name: "backend-data.tar", Method: zip.Store})
	if err != nil {
		return Backup{}, err
	}
	if err := m.compose(m.composePath(), version, m.config.StepTimeout, dataEntry, "exec", "-T", "--user", "root", "backend", "tar", "-C", "/data", "-cf", "-", "."); err != nil {
		return Backup{}, fmt.Errorf("备份数据目录：%w", err)
	}
	if err := archive.Close(); err != nil {
		return Backup{}, fmt.Errorf("完成 ZIP 备份：%w", err)
	}
	if err := file.Sync(); err != nil {
		return Backup{}, fmt.Errorf("同步 ZIP 备份：%w", err)
	}
	if err := file.Close(); err != nil {
		return Backup{}, err
	}
	if err := os.Rename(temporary, path); err != nil {
		return Backup{}, fmt.Errorf("提交 ZIP 备份：%w", err)
	}
	removeTemporary = false
	stat, err := os.Stat(path)
	if err != nil {
		return Backup{}, err
	}
	checksum := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if err := verifyZipBackup(path, checksum); err != nil {
		return Backup{}, err
	}
	return Backup{ID: id, Path: path, Checksum: checksum, Size: stat.Size(), CreatedAt: now, Version: version}, nil
}

func writeZipBytes(writer *zip.Writer, name string, data []byte) error {
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

func verifyZipBackup(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	actual := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return fmt.Errorf("ZIP 备份校验失败：期望 %s，实际 %s", expected, actual)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("ZIP 备份无法打开：%w", err)
	}
	defer archive.Close()
	required := map[string]bool{"metadata.json": false, "database.dump": false, "backend-data.tar": false}
	for _, entry := range archive.File {
		if _, ok := required[entry.Name]; ok && entry.UncompressedSize64 > 0 {
			required[entry.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("ZIP 备份缺少有效文件 %s", name)
		}
	}
	return nil
}

func (m *Manager) restoreDatabase(backup Backup) error {
	if err := verifyZipBackup(backup.Path, backup.Checksum); err != nil {
		return fmt.Errorf("拒绝恢复未通过校验的备份：%w", err)
	}
	archive, err := zip.OpenReader(backup.Path)
	if err != nil {
		return err
	}
	defer archive.Close()
	var dump *zip.File
	for _, entry := range archive.File {
		if entry.Name == "database.dump" {
			dump = entry
			break
		}
	}
	if dump == nil {
		return errors.New("备份中不存在 database.dump")
	}
	reader, err := dump.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	values, err := readEnvFile(m.envPath())
	if err != nil {
		return err
	}
	postgresUser := firstNonEmpty(values["POSTGRES_USER"], "open_ai_canvas")
	postgresDB := firstNonEmpty(values["POSTGRES_DB"], "open_ai_canvas")
	if postgresDB == "postgres" || strings.HasPrefix(postgresDB, "template") {
		return fmt.Errorf("拒绝覆盖 PostgreSQL 系统数据库 %q", postgresDB)
	}
	if err := m.compose(m.composePath(), backup.Version, 5*time.Minute, nil, "exec", "-T", "postgres", "dropdb", "-U", postgresUser, "--if-exists", "--force", postgresDB); err != nil {
		return fmt.Errorf("删除待恢复数据库：%w", err)
	}
	if err := m.compose(m.composePath(), backup.Version, 5*time.Minute, nil, "exec", "-T", "postgres", "createdb", "-U", postgresUser, "-O", postgresUser, postgresDB); err != nil {
		return fmt.Errorf("重建待恢复数据库：%w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.config.StepTimeout)
	defer cancel()
	args := []string{"compose", "--env-file", m.envPath(), "-f", m.composePath(), "exec", "-T", "postgres", "pg_restore", "-U", postgresUser, "-d", postgresDB, "--no-owner", "--no-privileges"}
	var stderr bytes.Buffer
	command := execCommandWithInput{runner: m.runner, input: reader}
	if err := command.Run(ctx, "docker", args, []string{"CANVAS_IMAGE_TAG=" + strings.TrimPrefix(backup.Version, "v")}, io.Discard, &stderr); err != nil {
		return fmt.Errorf("恢复 PostgreSQL：%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

type execCommandWithInput struct {
	runner commandRunner
	input  io.Reader
}

func (c execCommandWithInput) Run(ctx context.Context, name string, args, environment []string, stdout, stderr io.Writer) error {
	runner, ok := c.runner.(execRunner)
	if !ok {
		return errors.New("当前命令执行器不支持标准输入")
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = runner.dir
	command.Env = append(os.Environ(), environment...)
	command.Stdin = c.input
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func (m *Manager) verifyHealthy(targetVersion string) error {
	healthURL := m.healthURL()
	deadline := time.Now().Add(10 * time.Minute)
	stableSince := time.Time{}
	for time.Now().Before(deadline) {
		if err := m.checkHealthOnce(healthURL, targetVersion); err != nil {
			stableSince = time.Time{}
		} else if stableSince.IsZero() {
			stableSince = time.Now()
		} else if time.Since(stableSince) >= m.config.StableWindow {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("目标版本 %s 未在健康检查窗口内稳定就绪", targetVersion)
}

func (m *Manager) healthURL() string {
	if configured := strings.TrimSpace(m.config.HealthURL); configured != "" {
		return configured
	}
	values, err := readEnvFile(m.envPath())
	if err != nil {
		return "http://127.0.0.1:3000/api/health/ready"
	}
	return "http://127.0.0.1:" + firstNonEmpty(values["CANVAS_HTTP_PORT"], "3000") + "/api/health/ready"
}

func (m *Manager) checkHealthOnce(healthURL, targetVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	response, err := m.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("健康接口返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Build struct {
				Version string `json:"version"`
			} `json:"build"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&payload); err != nil {
		return err
	}
	if payload.Code != 0 {
		return errors.New("健康接口业务状态异常")
	}
	if IsReleaseVersion(targetVersion) && payload.Data.Build.Version != "" && CompareVersions(payload.Data.Build.Version, targetVersion) != 0 {
		return fmt.Errorf("运行版本仍为 %s，期望 %s", payload.Data.Build.Version, targetVersion)
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s：%w", path, err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"")
		}
	}
	return values, nil
}

type envUpdate struct {
	key   string
	value string
}

func setEnvValue(path, key, value string) error {
	return setEnvValues(path, envUpdate{key: key, value: value})
}

func setEnvValues(path string, updates ...envUpdate) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	lines := []string{}
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	values := make(map[string]string, len(updates))
	for _, update := range updates {
		if strings.TrimSpace(update.key) == "" {
			return errors.New("环境变量名不能为空")
		}
		values[update.key] = update.value
	}
	found := make(map[string]bool, len(values))
	output := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		key, _, ok := strings.Cut(trimmed, "=")
		if ok {
			if value, changed := values[key]; changed {
				if !found[key] {
					output = append(output, key+"="+value)
					found[key] = true
				}
				continue
			}
		}
		output = append(output, line)
	}
	for _, update := range updates {
		if !found[update.key] {
			output = append(output, update.key+"="+update.value)
			found[update.key] = true
		}
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	return writeAtomicFile(path, []byte(strings.Join(output, "\n")+"\n"), stat.Mode().Perm())
}

func replaceFile(source, target string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if mode == 0 {
		if stat, statErr := os.Stat(target); statErr == nil {
			mode = stat.Mode().Perm()
		} else {
			mode = 0o600
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".compose-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, target)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
