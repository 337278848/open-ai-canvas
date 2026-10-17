package app

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDecryptSettingSecretAcceptsPaddedBase64(t *testing.T) {
	svc := newSettingsSecretService(t)
	const secret = "mk-test-channel-secret"
	raw, err := svc.encryptSettingSecret(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	payload := strings.TrimPrefix(raw, encryptedSettingPrefix)
	padded := encryptedSettingPrefix + base64.StdEncoding.EncodeToString(mustDecodeRawBase64(t, payload))
	if padded == raw {
		t.Fatal("fixture did not exercise padded base64")
	}

	got, err := svc.decryptSettingSecret(padded)
	if err != nil {
		t.Fatalf("decrypt padded: %v", err)
	}
	if got != secret {
		t.Fatalf("decrypt padded = %q", got)
	}
	got, err = svc.decryptSettingSecret(raw)
	if err != nil || got != secret {
		t.Fatalf("decrypt raw = %q, %v", got, err)
	}
}

func TestDecryptSettingSecretRejectsTruncatedBase64(t *testing.T) {
	svc := newSettingsSecretService(t)
	raw, err := svc.encryptSettingSecret("mk-test-channel-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	payload := strings.TrimPrefix(raw, encryptedSettingPrefix)
	if len(payload) < 4 {
		t.Fatal("ciphertext unexpectedly short")
	}
	_, err = svc.decryptSettingSecret(encryptedSettingPrefix + payload[:len(payload)-2])
	if err == nil || !strings.Contains(err.Error(), "格式无效") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveProviderConfigPreservesChannelReadError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "channel-read.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ModelChannel{}); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	channel := model.ModelChannel{ID: "CHANNEL_PADDED", Scope: model.ChannelScopeSystem, Enabled: true, Name: "Padded", BaseURL: "https://example.invalid", APIKey: "enc:v1:%%%%", APIFormat: "openai"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repository.New(db), dataDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(svc.dataDir, ".settings-key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = svc.resolveProviderConfig(providerConfig{ChannelID: channel.ID, BaseURL: channel.BaseURL, Model: "MiniMax-H3"})
	if err == nil || errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "不存在或已停用") || !strings.Contains(err.Error(), "密文格式无效") {
		t.Fatalf("error = %v", err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

func newSettingsSecretService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".settings-key"), key, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return &Service{dataDir: dir}
}

func mustDecodeRawBase64(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return decoded
}
