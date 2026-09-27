package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"infinite-canvas/backend/internal/database"
	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestMigrationListCoversSchemaModels(t *testing.T) {
	want := make(map[string]bool, len(database.Models()))
	cache := &sync.Map{}
	for _, value := range database.Models() {
		parsed, err := schema.Parse(value, cache, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		want[parsed.Table] = false
	}
	for _, migration := range migrations() {
		if _, exists := want[migration.name]; !exists {
			t.Fatalf("migration list contains unknown table %q", migration.name)
		}
		if want[migration.name] {
			t.Fatalf("migration list contains duplicate table %q", migration.name)
		}
		want[migration.name] = true
	}
	for table, covered := range want {
		if !covered {
			t.Errorf("migration list is missing table %q", table)
		}
	}
}

func TestCanvasHistoryCompositeKeyMigration(t *testing.T) {
	source, err := database.Open(database.Config{Driver: "sqlite", DSN: "file:history-migration-source?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.Open(database.Config{Driver: "sqlite", DSN: "file:history-migration-target?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []*gorm.DB{source, target} {
		if err := db.AutoMigrate(&model.Resource{}, &model.CanvasSnapshotResource{}); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Resource{ID: "resource"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	refs := []model.CanvasSnapshotResource{{SnapshotID: "b", ResourceID: "resource"}, {SnapshotID: "a", ResourceID: "resource"}}
	if err := source.Create(&refs).Error; err != nil {
		t.Fatal(err)
	}
	count, err := migrateTable[model.CanvasSnapshotResource]("canvas_snapshot_resources").run(source, target, true)
	if err != nil || count != 2 {
		t.Fatalf("history refs not copied/verified: %d %v", count, err)
	}
}

func TestGeminiCacheMigrationCopiesRows(t *testing.T) {
	source, err := database.Open(database.Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "source.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.Open(database.Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "target.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceSQL, err := source.DB()
	if err != nil {
		t.Fatal(err)
	}
	targetSQL, err := target.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceSQL.Close() })
	t.Cleanup(func() { _ = targetSQL.Close() })
	for _, db := range []*gorm.DB{source, target} {
		if err := db.AutoMigrate(&model.CloudAgentGeminiCache{}); err != nil {
			t.Fatal(err)
		}
	}

	expireTime := time.Date(2026, time.September, 26, 12, 34, 56, 0, time.UTC)
	want := []model.CloudAgentGeminiCache{
		{
			ID:             "cache-1",
			UserID:         "user-1",
			CacheKey:       "same-key",
			BaseURL:        "https://generativelanguage.googleapis.com",
			Model:          "gemini-2.5-pro",
			CredentialHash: "credential-hash-1",
			ResourceName:   "cachedContents/cache-1",
			ExpireTime:     expireTime,
		},
		{
			ID:             "cache-2",
			UserID:         "user-2",
			CacheKey:       "same-key",
			BaseURL:        "https://generativelanguage.googleapis.com",
			Model:          "gemini-2.5-pro",
			CredentialHash: "credential-hash-2",
			ResourceName:   "cachedContents/cache-2",
			ExpireTime:     expireTime.Add(time.Hour),
		},
	}
	if err := source.Create(&want).Error; err != nil {
		t.Fatal(err)
	}

	var cacheMigration tableMigration
	for _, migration := range migrations() {
		if migration.name == "cloud_agent_gemini_caches" {
			cacheMigration = migration
			break
		}
	}
	if cacheMigration.run == nil {
		t.Fatal("migration list is missing cloud_agent_gemini_caches")
	}
	count, err := cacheMigration.run(source, target, true)
	if err != nil {
		t.Fatalf("copy Gemini cache rows: %v", err)
	}
	if count != len(want) {
		t.Fatalf("copied %d Gemini cache rows, want %d", count, len(want))
	}

	var got []model.CloudAgentGeminiCache
	if err := target.Order("id").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("target has %d Gemini cache rows, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].ID != want[index].ID ||
			got[index].UserID != want[index].UserID ||
			got[index].CacheKey != want[index].CacheKey ||
			got[index].BaseURL != want[index].BaseURL ||
			got[index].Model != want[index].Model ||
			got[index].CredentialHash != want[index].CredentialHash ||
			got[index].ResourceName != want[index].ResourceName ||
			!got[index].ExpireTime.Equal(want[index].ExpireTime) {
			t.Fatalf("target row %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}
