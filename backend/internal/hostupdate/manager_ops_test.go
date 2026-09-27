package hostupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type recordingRunner struct {
	calls [][]string
}

func (r *recordingRunner) Run(_ context.Context, _ string, args, _ []string, stdout, _ io.Writer) error {
	r.calls = append(r.calls, append([]string(nil), args...))
	if stdout != nil {
		_, _ = io.WriteString(stdout, "backup-fixture")
	}
	return nil
}

func TestSetEnvValuePreservesOtherSettings(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	if err := os.WriteFile(path, []byte("# keep\nCANVAS_IMAGE_TAG=1.0.0\nPOSTGRES_DB=canvas\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := setEnvValue(path, "CANVAS_IMAGE_TAG", "1.2.2-preview.1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value := string(data)
	if !strings.Contains(value, "# keep\n") || !strings.Contains(value, "POSTGRES_DB=canvas\n") || !strings.Contains(value, "CANVAS_IMAGE_TAG=1.2.2-preview.1\n") {
		t.Fatalf("unexpected env contents: %q", value)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose POSIX permission bits; the mode argument to
	// os.WriteFile is not preserved there.
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%o, want 640", stat.Mode().Perm())
	}
}

func TestVerifyZipBackupRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, content := range map[string]string{
		"metadata.json":    "{}",
		"database.dump":    "database",
		"backend-data.tar": "data",
	} {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := io.WriteString(entry, content); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	checksum := "sha256:" + hex.EncodeToString(hash[:])
	if err := verifyZipBackup(path, checksum); err != nil {
		t.Fatalf("valid backup rejected: %v", err)
	}
	if err := os.WriteFile(path, append(data, byte(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyZipBackup(path, checksum); err == nil {
		t.Fatal("corrupted backup was accepted")
	}
}

func TestCurrentVersionRejectsLatest(t *testing.T) {
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, ".env"), []byte("CANVAS_IMAGE_TAG=latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{config: Config{InstallDir: installDir, EnvFile: ".env"}}
	if _, err := manager.currentVersion(); err == nil {
		t.Fatal("latest tag was accepted")
	}
}

func TestLegacyReleaseVersionsAreRejectedBeforeUpdate(t *testing.T) {
	for _, version := range []string{"v1.2.9", "1.5.7", "v1.5.7.1"} {
		if !isLegacyReleaseVersion(version) {
			t.Fatalf("legacy release %q was not identified", version)
		}
	}
	for _, version := range []string{"v1.5.7.2", "v1.6.0", "main"} {
		if isLegacyReleaseVersion(version) {
			t.Fatalf("non-legacy release %q was rejected", version)
		}
	}
}

func TestDeploymentImageRepositoryPreservesCustomRepositoryName(t *testing.T) {
	imageRepository, err := deploymentImageRepository("acme/canvas")
	if err != nil {
		t.Fatalf("custom repository rejected: %v", err)
	}
	if imageRepository != "ghcr.io/acme/canvas" {
		t.Fatalf("image repository=%q, want ghcr.io/acme/canvas", imageRepository)
	}
	images := immutableImageRefs("acme/canvas", "v1.6.0")
	if images.backend != "ghcr.io/acme/canvas-backend:1.6.0" ||
		images.web != "ghcr.io/acme/canvas-web:1.6.0" {
		t.Fatalf("custom immutable refs=%+v", images)
	}
}

func TestManagerUsesConfiguredCustomImageRepository(t *testing.T) {
	manager := &Manager{config: Config{
		Repository:      "ddcat-ai/open-ai-canvas",
		ImageRepository: "ghcr.io/acme/canvas",
	}}
	imageRepository, err := manager.imageRepository()
	if err != nil {
		t.Fatalf("configured image repository rejected: %v", err)
	}
	if imageRepository != "ghcr.io/acme/canvas" {
		t.Fatalf("manager image repository=%q, want ghcr.io/acme/canvas", imageRepository)
	}
	images, err := manager.immutableImageRefs("v1.6.0")
	if err != nil {
		t.Fatal(err)
	}
	if images.backend != "ghcr.io/acme/canvas-backend:1.6.0" ||
		images.web != "ghcr.io/acme/canvas-web:1.6.0" {
		t.Fatalf("manager custom immutable refs=%+v", images)
	}
}

func TestStartUpdateRejectsLegacyReleaseBeforeStateWrite(t *testing.T) {
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, ".env"), []byte("CANVAS_IMAGE_TAG=1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		config: Config{InstallDir: installDir, EnvFile: ".env"},
		state: persistedState{
			LatestRelease: &Release{Version: "v1.5.7.1"},
			Operation:     Operation{Phase: PhaseReady},
		},
	}
	before := manager.state.Operation
	if _, err := manager.StartUpdate("v1.5.7.1"); err == nil || !strings.Contains(err.Error(), "tag-based legacy") {
		t.Fatalf("legacy update was not rejected clearly: %v", err)
	}
	if manager.state.Operation.Phase != before.Phase ||
		manager.state.Operation.TargetVersion != before.TargetVersion ||
		len(manager.state.Operation.Logs) != len(before.Logs) {
		t.Fatalf("legacy rejection mutated operation state: before=%+v after=%+v", before, manager.state.Operation)
	}
}

func TestCreateBackupReadsBackendDataAsRoot(t *testing.T) {
	installDir := t.TempDir()
	backupDir := filepath.Join(installDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, ".env"), []byte(
		"POSTGRES_USER=canvas\nPOSTGRES_DB=canvas\n"+
			"CANVAS_IMAGE_TAG=1.2.2-preview.2\n"+
			"CANVAS_BACKEND_IMAGE=ghcr.io/ddcat-ai/open-ai-canvas-backend@sha256:"+strings.Repeat("a", 64)+"\n"+
			"CANVAS_WEB_IMAGE=ghcr.io/ddcat-ai/open-ai-canvas-web@sha256:"+strings.Repeat("b", 64)+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	manager := &Manager{
		config: Config{Repository: "ddcat-ai/open-ai-canvas", InstallDir: installDir, ComposeFile: "docker-compose.deploy.yml", EnvFile: ".env", BackupDir: backupDir},
		runner: runner,
	}
	if _, err := manager.createBackup("v1.2.2-preview.2"); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "exec -T --user root backend tar -C /data -cf - .") {
			return
		}
	}
	t.Fatalf("backend data backup did not use root: %#v", runner.calls)
}

func TestCheckWritableDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := checkWritableDirectory(directory); err != nil {
		t.Fatalf("writable directory rejected: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("write probe was not cleaned up: %v", entries)
	}
	if err := checkWritableDirectory(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("missing directory was accepted")
	}
}

func TestValidateComposeImageContract(t *testing.T) {
	valid := []byte(`
services:
  migrate:
    image: ${CANVAS_BACKEND_IMAGE:?请先配置}
  backend:
    image: "${CANVAS_BACKEND_IMAGE}"
  web:
    image: ${CANVAS_WEB_IMAGE:?请先配置}
`)
	if err := validateComposeImageContract(valid); err != nil {
		t.Fatalf("valid image contract rejected: %v", err)
	}

	testCases := map[string][]byte{
		"legacy tags": []byte(`
services:
  migrate:
    image: ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  backend:
    image: ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  web:
    image: ghcr.io/ddcat-ai/open-ai-canvas-web:${CANVAS_IMAGE_TAG:-latest}
`),
		"comment only": []byte(`
# image: ${CANVAS_BACKEND_IMAGE}
# image: ${CANVAS_WEB_IMAGE}
`),
		"unrelated image lines": []byte(`
services:
  postgres:
    image: ${CANVAS_BACKEND_IMAGE}
  migrate:
    image: ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  backend:
    image: ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  web:
    image: ghcr.io/ddcat-ai/open-ai-canvas-web:${CANVAS_IMAGE_TAG:-latest}
`),
		"similar variable name": []byte(`
services:
  migrate:
    image: ${CANVAS_BACKEND_IMAGE_NAME}
  backend:
    image: ${CANVAS_BACKEND_IMAGE_NAME}
  web:
    image: ${CANVAS_WEB_IMAGE_NAME}
`),
	}
	for name, data := range testCases {
		t.Run(name, func(t *testing.T) {
			if err := validateComposeImageContract(data); err == nil {
				t.Fatal("legacy or incomplete image contract was accepted")
			}
		})
	}
}

func TestNormalizeHistoricalV1571ComposeFixture(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件")
	}
	fixturePath := filepath.Join(filepath.Dir(testFile), "testdata", "v1.5.7.1", "docker-compose.deploy.yml")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("读取历史 Compose fixture：%v", err)
	}
	normalized, err := normalizeComposeImageContractForRepository(data, defaultDeploymentImageRepository)
	if err != nil {
		t.Fatalf("legacy Compose normalization failed: %v", err)
	}
	text := string(normalized)
	for _, legacy := range []string{
		"ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}",
		"ghcr.io/ddcat-ai/open-ai-canvas-web:${CANVAS_IMAGE_TAG:-latest}",
	} {
		if strings.Contains(text, legacy) {
			t.Fatalf("normalized fixture still contains legacy image reference %q", legacy)
		}
	}
	for _, required := range []string{
		"image: ${CANVAS_BACKEND_IMAGE:?请先配置 CANVAS_BACKEND_IMAGE}",
		"image: ${CANVAS_WEB_IMAGE:?请先配置 CANVAS_WEB_IMAGE}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("normalized fixture is missing %q", required)
		}
	}
	if err := validateComposeImageContract(normalized); err != nil {
		t.Fatalf("normalized fixture failed canonical validation: %v", err)
	}
}

func TestNormalizeLegacyComposeForCustomRepository(t *testing.T) {
	data := []byte(`
services:
  migrate:
    image: ghcr.io/acme/canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  backend:
    image: ghcr.io/acme/canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  web:
    image: ghcr.io/acme/canvas-web:${CANVAS_IMAGE_TAG:-latest}
`)
	normalized, err := normalizeComposeImageContractForRepository(data, "ghcr.io/acme/canvas")
	if err != nil {
		t.Fatalf("custom legacy Compose normalization failed: %v", err)
	}
	text := string(normalized)
	for _, required := range []string{
		"image: ${CANVAS_BACKEND_IMAGE:?请先配置 CANVAS_BACKEND_IMAGE}",
		"image: ${CANVAS_WEB_IMAGE:?请先配置 CANVAS_WEB_IMAGE}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("custom normalized Compose is missing %q", required)
		}
	}
}

func TestImageInstallerInstallsUpdaterBeforeBackend(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件")
	}
	scriptPath := filepath.Join(filepath.Dir(testFile), "..", "..", "..", "scripts", "install-server-image.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读取镜像安装脚本：%v", err)
	}
	source := string(data)
	mainStart := strings.LastIndex(source, "\nmain() {")
	if mainStart < 0 {
		t.Fatal("镜像安装脚本缺少 main 入口")
	}
	main := source[mainStart:]
	previous := -1
	for _, step := range []string{"pull_and_pin_images", "install_host_updater", "start_services"} {
		position := strings.Index(main, step)
		if position <= previous {
			t.Fatalf("安装顺序错误：%s 出现在前一个步骤之前或缺失", step)
		}
		previous = position
	}
}
