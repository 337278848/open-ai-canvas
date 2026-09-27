package database

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

func TestCurrentSchemaVersionMatchesMigrationPlan(t *testing.T) {
	if len(schemaMigrations) == 0 {
		t.Fatal("migration plan is empty")
	}
	latest := schemaMigrations[len(schemaMigrations)-1].version
	if CurrentSchemaVersion != latest {
		t.Fatalf("supported schema version %d does not match latest migration %d", CurrentSchemaVersion, latest)
	}
}

func TestSchema42MigrationSuffixPreservesPublishedMetadata(t *testing.T) {
	expected := map[int64]struct {
		name     string
		checksum string
	}{
		32: {"tool_schema_reconciliation", toolSchemaReconciliationChecksum},
		33: {"channel_model_tags", channelModelTagsChecksum},
		34: {"lxmone_h3_capability_limits", lxmoneH3CapabilityChecksum},
		35: {"provider_request_id_reconciliation", providerRequestIDReconciliationChecksum},
		36: {"capability_provider_reconciliation_followup", reconciliationFollowupChecksum},
		37: {"oauth_state_accepted_terms", oauthStateAcceptedTermsChecksum},
		38: {"task_media_recovery", taskMediaRecoveryChecksum},
		39: {"auth_notifications", authNotificationsChecksum},
		40: {"cloud_agent_gemini_cache", "sha256:cloud-agent-gemini-cache-v40-20260926"},
		41: {"cloud_agent_gemini_cache_identity", "sha256:cloud-agent-gemini-cache-identity-v41-20260926"},
		42: {"prefixed_id_sequence_reconcile", "sha256:prefixed-id-sequence-reconcile-v42-20260926"},
	}
	for _, item := range schemaMigrations {
		want, ok := expected[item.version]
		if !ok {
			continue
		}
		if item.name != want.name || item.checksum != want.checksum {
			t.Fatalf("migration %d = %s/%s, want %s/%s", item.version, item.name, item.checksum, want.name, want.checksum)
		}
		delete(expected, item.version)
	}
	if len(expected) != 0 {
		t.Fatalf("migration suffix missing versions: %#v", expected)
	}
}

func TestMigrateSchemaRecordsAndValidatesVersion(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-version?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected schema status: %#v", status)
	}
	if !db.Migrator().HasIndex(&schemaMigration{}, "idx_schema_migrations_applied_at") {
		t.Fatal("schema migration v2 did not create the applied_at index")
	}
	if !db.Migrator().HasIndex(&model.ProjectAssetCandidate{}, "idx_project_asset_candidates_pending_identity") {
		t.Fatal("schema migration v3 did not create candidate identity index")
	}
	if !db.Migrator().HasTable(&model.AgentProfile{}) || !db.Migrator().HasIndex(&model.AgentProfile{}, "idx_agent_profiles_scope") {
		t.Fatal("schema migration v15 did not create scoped Agent profiles")
	}
	if !db.Migrator().HasTable(&model.AgentLesson{}) || !db.Migrator().HasIndex(&model.AgentLesson{}, "idx_agent_lessons_status") {
		t.Fatal("schema migration v16 did not create Agent lessons")
	}
	if !db.Migrator().HasIndex(&model.AgentLesson{}, "idx_agent_lessons_author_status") {
		t.Fatal("schema migration v17 did not create owner status index")
	}
	if !db.Migrator().HasTable(&model.AgentMemorySetting{}) {
		t.Fatal("schema migration v18 did not create agent memory settings")
	}
	if !db.Migrator().HasColumn(&model.PaymentProviderConfig{}, "plugin_version") || !db.Migrator().HasColumn(&model.PaymentOrder{}, "plugin_version") {
		t.Fatal("schema migration v19 did not add payment plugin version columns")
	}
	if !db.Migrator().HasTable(&model.BannerAnnouncement{}) {
		t.Fatal("schema migration v20 did not create banner announcements")
	}
	if !db.Migrator().HasColumn(&model.BannerAnnouncement{}, "title_runs") {
		t.Fatal("schema migration v21 did not create banner announcements title_runs")
	}
	if !db.Migrator().HasColumn(&model.BannerAnnouncement{}, "notice_type") {
		t.Fatal("schema migration v22 did not create banner announcements notice_type")
	}
	if !db.Migrator().HasTable(&model.AuthVerification{}) ||
		!db.Migrator().HasTable(&model.NotificationQuota{}) ||
		!db.Migrator().HasTable(&model.SMSChannel{}) ||
		!db.Migrator().HasTable(&model.SMSRecord{}) {
		t.Fatal("schema migration v39 did not create auth notification tables")
	}
	if !db.Migrator().HasColumn(&model.User{}, "phone") ||
		!db.Migrator().HasColumn(&model.User{}, "email_verified_at") ||
		!db.Migrator().HasColumn(&model.User{}, "phone_verified_at") ||
		!db.Migrator().HasColumn(&model.EmailVerificationCode{}, "attempts") {
		t.Fatal("schema migration v39 did not add authentication verification fields")
	}
	if !db.Migrator().HasTable(&model.CloudAgentGeminiCache{}) {
		t.Fatal("schema migration v40 did not create Gemini cache table")
	}
	if !db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_cache_user_key") {
		t.Fatal("schema migration v41 did not create user-scoped Gemini cache identity")
	}
	if !db.Migrator().HasTable(&model.IDSequence{}) {
		t.Fatal("schema migration v42 did not create readable ID sequence table")
	}
	var sequence model.IDSequence
	if err := db.First(&sequence, "name = ?", "id:CHANNEL").Error; err != nil {
		t.Fatalf("schema migration v42 did not reconcile CHANNEL sequence: %v", err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration should be idempotent: %v", err)
	}
}

func TestMigrateSchemaV39UpgradesExistingDatabase(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-auth-notifications-v39?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}

	for _, table := range []any{&model.AuthVerification{}, &model.NotificationQuota{}, &model.SMSChannel{}, &model.SMSRecord{}} {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"phone", "email_verified_at", "phone_verified_at"} {
		if err := db.Migrator().DropColumn(&model.User{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrator().DropColumn(&model.EmailVerificationCode{}, "attempts"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 39).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v38: %v", err)
	}
	for _, table := range []any{&model.AuthVerification{}, &model.NotificationQuota{}, &model.SMSChannel{}, &model.SMSRecord{}} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("migration v39 did not restore table %T", table)
		}
	}
	for _, column := range []string{"phone", "email_verified_at", "phone_verified_at"} {
		if !db.Migrator().HasColumn(&model.User{}, column) {
			t.Fatalf("migration v39 did not restore users.%s", column)
		}
	}
	if !db.Migrator().HasColumn(&model.EmailVerificationCode{}, "attempts") {
		t.Fatal("migration v39 did not restore email verification attempts")
	}
}

func TestMigrateCloudAgentGeminiCacheIdentityDropsGlobalCacheKeyIndex(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-gemini-cache-identity-v41?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudAgentGeminiCache{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP INDEX IF EXISTS idx_cloud_agent_gemini_cache_user_key").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_cloud_agent_gemini_caches_cache_key ON cloud_agent_gemini_caches(cache_key)").Error; err != nil {
		t.Fatal(err)
	}

	if err := migrateCloudAgentGeminiCacheIdentity(db); err != nil {
		t.Fatalf("reconcile Gemini cache identity: %v", err)
	}
	if !db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_cache_user_key") {
		t.Fatal("composite Gemini cache identity index missing")
	}
	if db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_caches_cache_key") {
		t.Fatal("global Gemini cache key index was not removed")
	}

	expiry := time.Now().Add(time.Hour)
	for _, item := range []model.CloudAgentGeminiCache{
		{ID: "cache-user-a", UserID: "user-a", CacheKey: "same-key", BaseURL: "https://example.test", Model: "gemini", CredentialHash: "hash-a", ResourceName: "cachedContents/a", ExpireTime: expiry},
		{ID: "cache-user-b", UserID: "user-b", CacheKey: "same-key", BaseURL: "https://example.test", Model: "gemini", CredentialHash: "hash-b", ResourceName: "cachedContents/b", ExpireTime: expiry},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("same cache key must be reusable across users: %v", err)
		}
	}
	if err := db.Create(&model.CloudAgentGeminiCache{
		ID: "cache-user-a-duplicate", UserID: "user-a", CacheKey: "same-key",
		BaseURL: "https://example.test", Model: "gemini", CredentialHash: "hash-a",
		ResourceName: "cachedContents/duplicate", ExpireTime: expiry,
	}).Error; err == nil {
		t.Fatal("same user/cache key must remain unique")
	}
}

func TestMigrateSchemaPostgresV38To42ReplacesGlobalGeminiCacheIndex(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CANVAS_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CANVAS_TEST_POSTGRES_DSN is not configured")
	}

	base, err := Open(Config{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatalf("postgres sql db: %v", err)
	}
	defer baseSQL.Close()

	schemaName := fmt.Sprintf("gemini_cache_v38_to_42_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA "` + schemaName + `"`).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if err := base.Exec(`DROP SCHEMA IF EXISTS "` + schemaName + `" CASCADE`).Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()

	testDSN, err := postgresDSNWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatalf("test postgres dsn: %v", err)
	}
	db, err := Open(Config{Driver: "postgres", DSN: testDSN})
	if err != nil {
		t.Fatalf("open test schema: %v", err)
	}
	dbSQL, err := db.DB()
	if err != nil {
		t.Fatalf("test schema sql db: %v", err)
	}
	defer dbSQL.Close()

	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatalf("create schema migration table: %v", err)
	}
	appliedAt := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	record := func(item migration) {
		t.Helper()
		if err := db.Create(&schemaMigration{
			Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: appliedAt,
		}).Error; err != nil {
			t.Fatalf("record migration %d: %v", item.version, err)
		}
	}

	for _, item := range schemaMigrations[:31] {
		if err := item.apply(db); err != nil {
			t.Fatalf("apply common migration %d: %v", item.version, err)
		}
		record(item)
	}
	if err := db.Create(&model.ModelChannel{ID: "CHANNEL_000321"}).Error; err != nil {
		t.Fatalf("create deterministic channel: %v", err)
	}

	upstream := []migration{
		{version: 32, name: "channel_model_tags", checksum: legacyChannelModelTagsChecksum, apply: migrateChannelModelTags},
		{version: 33, name: "oauth_state_accepted_terms", checksum: legacyOAuthStateAcceptedTermsChecksum, apply: migrateOAuthStateAcceptedTerms},
		{version: 34, name: "task_media_recovery", checksum: legacyTaskMediaRecoveryChecksum, apply: migrateTaskMediaRecovery},
		{version: 35, name: "auth_notifications", checksum: legacyAuthNotificationsChecksum, apply: migrateSchemaV35},
		{version: 36, name: "cloud_agent_gemini_cache", checksum: legacyCloudAgentGeminiCacheChecksum, apply: migrateCloudAgentGeminiCache},
		{version: 37, name: "cloud_agent_gemini_cache_identity", checksum: legacyCloudAgentGeminiCacheIdentityChecksum, apply: migrateCloudAgentGeminiCacheIdentity},
		{version: 38, name: "prefixed_id_sequence_reconcile", checksum: legacyPrefixedIDSequenceReconcileChecksum, apply: migratePrefixedIDSequenceReconcile},
	}
	for _, item := range upstream {
		if err := item.apply(db); err != nil {
			t.Fatalf("apply upstream migration %d: %v", item.version, err)
		}
		record(item)
	}

	if err := db.Exec(`DROP INDEX IF EXISTS "idx_cloud_agent_gemini_cache_user_key"`).Error; err != nil {
		t.Fatalf("drop composite Gemini cache index: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX "idx_cloud_agent_gemini_caches_cache_key" ON "cloud_agent_gemini_caches" ("cache_key")`).Error; err != nil {
		t.Fatalf("create legacy global Gemini cache index: %v", err)
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade PostgreSQL schema v38 to v42: %v", err)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected migrated status: %+v", status)
	}
	if db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_caches_cache_key") {
		t.Fatal("legacy global Gemini cache index still exists")
	}
	if !db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_cache_user_key") {
		t.Fatal("user-scoped Gemini cache index is missing")
	}

	expiry := time.Now().Add(time.Hour)
	for _, item := range []model.CloudAgentGeminiCache{
		{ID: "cache-user-a", UserID: "user-a", CacheKey: "same-key", BaseURL: "https://example.test", Model: "gemini", CredentialHash: "hash-a", ResourceName: "cachedContents/a", ExpireTime: expiry},
		{ID: "cache-user-b", UserID: "user-b", CacheKey: "same-key", BaseURL: "https://example.test", Model: "gemini", CredentialHash: "hash-b", ResourceName: "cachedContents/b", ExpireTime: expiry},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("same cache key must be reusable across users after PostgreSQL upgrade: %v", err)
		}
	}
	if err := db.Create(&model.CloudAgentGeminiCache{
		ID: "cache-user-a-duplicate", UserID: "user-a", CacheKey: "same-key",
		BaseURL: "https://example.test", Model: "gemini", CredentialHash: "hash-a",
		ResourceName: "cachedContents/duplicate", ExpireTime: expiry,
	}).Error; err == nil {
		t.Fatal("same user/cache key must remain unique after PostgreSQL upgrade")
	}
}

func TestMigrateSchemaV42ReconcilesPrefixedIDSequences(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-prefixed-id-sequence-v42?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ModelChannel{}, &model.IDSequence{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelChannel{ID: "CHANNEL_000123"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.IDSequence{Name: "id:CHANNEL", Value: 7}).Error; err != nil {
		t.Fatal(err)
	}

	if err := migratePrefixedIDSequenceReconcile(db); err != nil {
		t.Fatalf("reconcile prefixed ID sequence: %v", err)
	}
	var sequence model.IDSequence
	if err := db.First(&sequence, "name = ?", "id:CHANNEL").Error; err != nil {
		t.Fatal(err)
	}
	if sequence.Value != 123 {
		t.Fatalf("CHANNEL sequence = %d, want 123", sequence.Value)
	}

	if err := db.Model(&model.IDSequence{}).Where("name = ?", "id:CHANNEL").Update("value", 200).Error; err != nil {
		t.Fatal(err)
	}
	if err := migratePrefixedIDSequenceReconcile(db); err != nil {
		t.Fatalf("repeat sequence reconciliation: %v", err)
	}
	if err := db.First(&sequence, "name = ?", "id:CHANNEL").Error; err != nil {
		t.Fatal(err)
	}
	if sequence.Value != 200 {
		t.Fatalf("sequence regressed to %d, want 200", sequence.Value)
	}
}

func TestMigrateSchemaConvergesUpstreamSchema38Lineage(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-upstream-schema-38-to-42?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}
	appliedAt := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	record := func(version int64, name, checksum string) {
		t.Helper()
		if err := db.Create(&schemaMigration{
			Version: version, Name: name, Checksum: checksum, AppliedAt: appliedAt,
		}).Error; err != nil {
			t.Fatalf("record migration %d: %v", version, err)
		}
	}

	for _, item := range schemaMigrations[:31] {
		if err := item.apply(db); err != nil {
			t.Fatalf("apply common migration %d: %v", item.version, err)
		}
		record(item.version, item.name, item.checksum)
	}
	if err := db.Create(&model.ModelChannel{ID: "CHANNEL_000321"}).Error; err != nil {
		t.Fatal(err)
	}

	upstream := []struct {
		version  int64
		name     string
		checksum string
		apply    func(*gorm.DB) error
	}{
		{32, "channel_model_tags", legacyChannelModelTagsChecksum, migrateChannelModelTags},
		{33, "oauth_state_accepted_terms", legacyOAuthStateAcceptedTermsChecksum, migrateOAuthStateAcceptedTerms},
		{34, "task_media_recovery", legacyTaskMediaRecoveryChecksum, migrateTaskMediaRecovery},
		{35, "auth_notifications", legacyAuthNotificationsChecksum, migrateSchemaV35},
		{36, "cloud_agent_gemini_cache", legacyCloudAgentGeminiCacheChecksum, migrateCloudAgentGeminiCache},
		{37, "cloud_agent_gemini_cache_identity", legacyCloudAgentGeminiCacheIdentityChecksum, migrateCloudAgentGeminiCacheIdentity},
		{38, "prefixed_id_sequence_reconcile", legacyPrefixedIDSequenceReconcileChecksum, migratePrefixedIDSequenceReconcile},
	}
	for _, item := range upstream {
		if err := item.apply(db); err != nil {
			t.Fatalf("apply upstream migration %d: %v", item.version, err)
		}
		record(item.version, item.name, item.checksum)
	}
	// Simulate a partially merged build that recorded the canonical v40 row
	// after creating only the cache table. The compatibility pass must still
	// repair local-only schema work when the normal v40 loop skips this row.
	record(40, "cloud_agent_gemini_cache", cloudAgentGeminiCacheChecksum)

	var historyBefore [7]schemaMigration
	for index, version := range []int64{32, 33, 34, 35, 36, 37, 38} {
		if err := db.First(&historyBefore[index], "version = ?", version).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("DROP INDEX IF EXISTS idx_cloud_agent_gemini_cache_user_key").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_cloud_agent_gemini_caches_cache_key ON cloud_agent_gemini_caches(cache_key)").Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"RelayURL", "RelayExpiresAt"} {
		if db.Migrator().HasColumn(&model.Resource{}, field) {
			if err := db.Migrator().DropColumn(&model.Resource{}, field); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Model(&model.IDSequence{}).Where("name = ?", "id:CHANNEL").Update("value", 0).Error; err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("converge upstream schema 38: %v", err)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Current != 42 {
		t.Fatalf("unexpected converged status: %+v", status)
	}

	for index, version := range []int64{32, 33, 34, 35, 36, 37, 38} {
		var after schemaMigration
		if err := db.First(&after, "version = ?", version).Error; err != nil {
			t.Fatal(err)
		}
		if after.Name != historyBefore[index].Name ||
			after.Checksum != historyBefore[index].Checksum ||
			!after.AppliedAt.Equal(historyBefore[index].AppliedAt) {
			t.Fatalf("upstream migration %d was rewritten: before=%+v after=%+v", version, historyBefore[index], after)
		}
	}
	for _, item := range []migration{
		schemaMigrations[38], schemaMigrations[39], schemaMigrations[40], schemaMigrations[41],
	} {
		var applied schemaMigration
		if err := db.First(&applied, "version = ?", item.version).Error; err != nil {
			t.Fatalf("missing target migration %d: %v", item.version, err)
		}
		if applied.Name != item.name || applied.Checksum != item.checksum {
			t.Fatalf("target migration %d = %+v, want %s/%s", item.version, applied, item.name, item.checksum)
		}
	}
	if !db.Migrator().HasColumn(&model.Resource{}, "relay_url") ||
		!db.Migrator().HasColumn(&model.Resource{}, "relay_expires_at") {
		t.Fatal("v40 convergence did not restore local relay columns")
	}
	if !db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_cache_user_key") ||
		db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_caches_cache_key") {
		t.Fatal("v40 convergence did not restore user-scoped Gemini cache identity")
	}
	var sequence model.IDSequence
	if err := db.First(&sequence, "name = ?", "id:CHANNEL").Error; err != nil {
		t.Fatal(err)
	}
	if sequence.Value != 321 {
		t.Fatalf("v40 convergence sequence = %d, want 321", sequence.Value)
	}
	if err := migrateUpstreamLineageConvergence(db); err != nil {
		t.Fatalf("repeated v40 convergence is not idempotent: %v", err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("converged upstream schema is not idempotent: %v", err)
	}
}

func TestReadOnlySchemaValidationDoesNotRepairLatestSchema(t *testing.T) {
	for _, upstreamLineage := range []bool{false, true} {
		name := "canonical-lineage"
		if upstreamLineage {
			name = "upstream-lineage"
		}
		t.Run(name, func(t *testing.T) {
			db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared"})
			if err != nil {
				t.Fatal(err)
			}
			if err := MigrateSchema(db); err != nil {
				t.Fatal(err)
			}
			if upstreamLineage {
				for _, item := range []struct {
					version  int64
					name     string
					checksum string
				}{
					{32, "channel_model_tags", legacyChannelModelTagsChecksum},
					{33, "oauth_state_accepted_terms", legacyOAuthStateAcceptedTermsChecksum},
					{34, "task_media_recovery", legacyTaskMediaRecoveryChecksum},
					{35, "auth_notifications", legacyAuthNotificationsChecksum},
					{36, "cloud_agent_gemini_cache", legacyCloudAgentGeminiCacheChecksum},
					{37, "cloud_agent_gemini_cache_identity", legacyCloudAgentGeminiCacheIdentityChecksum},
					{38, "prefixed_id_sequence_reconcile", legacyPrefixedIDSequenceReconcileChecksum},
				} {
					if err := db.Model(&schemaMigration{}).Where("version = ?", item.version).Updates(map[string]any{
						"name": item.name, "checksum": item.checksum,
					}).Error; err != nil {
						t.Fatalf("rewrite upstream lineage record %d: %v", item.version, err)
					}
				}
			}
			if err := db.Create(&model.ModelChannel{ID: "CHANNEL_000321", Name: "keep"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.IDSequence{}).Where("name = ?", "id:CHANNEL").Update("value", 0).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().DropTable(&model.AuthVerification{}); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"RelayURL", "RelayExpiresAt"} {
				if db.Migrator().HasColumn(&model.Resource{}, field) {
					if err := db.Migrator().DropColumn(&model.Resource{}, field); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := db.Exec("DROP INDEX IF EXISTS idx_cloud_agent_gemini_cache_user_key").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("CREATE UNIQUE INDEX idx_cloud_agent_gemini_caches_cache_key ON cloud_agent_gemini_caches(cache_key)").Error; err != nil {
				t.Fatal(err)
			}
			historyBefore := make([]schemaMigration, 0, CurrentSchemaVersion)
			if err := db.Order("version ASC").Find(&historyBefore).Error; err != nil {
				t.Fatal(err)
			}
			var channelBefore model.ModelChannel
			if err := db.First(&channelBefore, "id = ?", "CHANNEL_000321").Error; err != nil {
				t.Fatal(err)
			}
			var sequenceBefore model.IDSequence
			if err := db.First(&sequenceBefore, "name = ?", "id:CHANNEL").Error; err != nil {
				t.Fatal(err)
			}

			status, err := ReadSchemaStatus(db)
			if err != nil {
				t.Fatalf("read schema status: %v", err)
			}
			if !status.Ready || status.Current != CurrentSchemaVersion {
				t.Fatalf("unexpected schema status: %+v", status)
			}
			if err := RequireSchemaVersion(db); err != nil {
				t.Fatalf("require schema version: %v", err)
			}

			if db.Migrator().HasTable(&model.AuthVerification{}) {
				t.Fatal("read-only validation recreated auth verification table")
			}
			if db.Migrator().HasColumn(&model.Resource{}, "relay_url") ||
				db.Migrator().HasColumn(&model.Resource{}, "relay_expires_at") {
				t.Fatal("read-only validation restored resource relay columns")
			}
			if db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_cache_user_key") {
				t.Fatal("read-only validation recreated Gemini cache index")
			}
			if !db.Migrator().HasIndex(&model.CloudAgentGeminiCache{}, "idx_cloud_agent_gemini_caches_cache_key") {
				t.Fatal("read-only validation removed the legacy Gemini cache index")
			}
			var channelAfter model.ModelChannel
			if err := db.First(&channelAfter, "id = ?", "CHANNEL_000321").Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(channelBefore, channelAfter) {
				t.Fatalf("read-only validation changed business data: before=%+v after=%+v", channelBefore, channelAfter)
			}
			var sequenceAfter model.IDSequence
			if err := db.First(&sequenceAfter, "name = ?", "id:CHANNEL").Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sequenceBefore, sequenceAfter) {
				t.Fatalf("read-only validation recalibrated ID sequence: before=%+v after=%+v", sequenceBefore, sequenceAfter)
			}
			historyAfter := make([]schemaMigration, 0, CurrentSchemaVersion)
			if err := db.Order("version ASC").Find(&historyAfter).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(historyBefore, historyAfter) {
				t.Fatalf("read-only validation changed schema_migrations: before=%+v after=%+v", historyBefore, historyAfter)
			}
		})
	}
}

func TestMigrateSchemaV15UpgradesExistingDatabase(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-agent-profiles-v15?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.AgentProfile{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 15).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v14: %v", err)
	}
	if !db.Migrator().HasTable(&model.AgentProfile{}) || !db.Migrator().HasIndex(&model.AgentProfile{}, "idx_agent_profiles_scope") {
		t.Fatal("v15 upgrade did not install Agent profile table and scope index")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV16UpgradesExistingDatabase(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-agent-lessons-v16?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.AgentLesson{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 16).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v15: %v", err)
	}
	if !db.Migrator().HasTable(&model.AgentLesson{}) || !db.Migrator().HasIndex(&model.AgentLesson{}, "idx_agent_lessons_status") {
		t.Fatal("v16 upgrade did not install Agent lesson table and status index")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV30AddsUpstreamRelayFields(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-resource-upstream-relay-v30?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.Resource{}, "RelayURL"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.Resource{}, "RelayExpiresAt"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 30).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("reconcile at v30: %v", err)
	}
	if !db.Migrator().HasColumn(&model.Resource{}, "relay_url") || !db.Migrator().HasColumn(&model.Resource{}, "relay_expires_at") {
		t.Fatal("v30 migration did not restore upstream relay fields")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaReconcilesLocalRelayAtV28(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-local-relay-v28?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	var original schemaMigration
	if err := db.First(&original, "version = ?", 28).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&schemaMigration{}).Where("version = ?", 28).Updates(map[string]any{
		"name":     "resource_upstream_relay",
		"checksum": resourceUpstreamRelayChecksum,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version >= ?", 29).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{&model.CloudAgentEventRecord{}, &model.CloudAgentMessageRecord{}, &model.CloudAgentResourceLease{}} {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"RelayURL", "RelayExpiresAt"} {
		if err := db.Migrator().DropColumn(&model.Resource{}, field); err != nil {
			t.Fatal(err)
		}
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from local relay v28: %v", err)
	}
	for _, table := range []any{&model.CloudAgentEventRecord{}, &model.CloudAgentMessageRecord{}, &model.CloudAgentResourceLease{}} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("local v28 reconciliation did not restore %T", table)
		}
	}
	if !db.Migrator().HasColumn(&model.Resource{}, "relay_url") || !db.Migrator().HasColumn(&model.Resource{}, "relay_expires_at") {
		t.Fatal("local v28 reconciliation did not restore relay fields")
	}
	var preserved schemaMigration
	if err := db.First(&preserved, "version = ?", 28).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.Name != "resource_upstream_relay" || preserved.Checksum != resourceUpstreamRelayChecksum || !preserved.AppliedAt.Equal(original.AppliedAt) {
		t.Fatalf("local v28 history was rewritten: %#v", preserved)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected reconciled schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaSupportsLegacyRelayAtV16(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-legacy-relay-v16?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.AgentLesson{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.Resource{}, "RelayURL"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.Resource{}, "RelayExpiresAt"); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&schemaMigration{}).Where("version = ?", 16).Updates(map[string]any{
		"name":     "resource_upstream_relay",
		"checksum": legacyResourceUpstreamRelayChecksum,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version >= ?", 17).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from legacy local schema v16: %v", err)
	}
	if !db.Migrator().HasTable(&model.AgentLesson{}) {
		t.Fatal("legacy v16 upgrade did not install Agent lessons")
	}
	if !db.Migrator().HasColumn(&model.Resource{}, "relay_url") || !db.Migrator().HasColumn(&model.Resource{}, "relay_expires_at") {
		t.Fatal("legacy v16 upgrade did not restore upstream relay fields")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected legacy-upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV17UpgradesExistingDatabase(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-agent-lessons-v17?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(&model.AgentLesson{}, "idx_agent_lessons_author_status"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 17).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v16: %v", err)
	}
	if !db.Migrator().HasIndex(&model.AgentLesson{}, "idx_agent_lessons_author_status") {
		t.Fatal("v17 upgrade did not install owner status index")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV18UpgradesExistingDatabase(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-agent-memory-settings-v18?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.AgentMemorySetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 18).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v17: %v", err)
	}
	if !db.Migrator().HasTable(&model.AgentMemorySetting{}) {
		t.Fatal("v18 upgrade did not install agent memory settings")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV19AddsPaymentPluginVersion(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-payment-plugin-version-v19?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.PaymentProviderConfig{}, "PluginVersion"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.PaymentOrder{}, "PluginVersion"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 19).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v18: %v", err)
	}
	if !db.Migrator().HasColumn(&model.PaymentProviderConfig{}, "plugin_version") {
		t.Fatal("v19 upgrade did not add payment_provider_configs.plugin_version")
	}
	if !db.Migrator().HasColumn(&model.PaymentOrder{}, "plugin_version") {
		t.Fatal("v19 upgrade did not add payment_orders.plugin_version")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV20UpgradesExistingDatabaseWithBannerAnnouncements(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-banner-announcements-v20?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&model.BannerAnnouncement{}) {
		t.Fatal("v20 migration did not create banner_announcements table")
	}
	// 模拟旧库升级：删表 + 删除 v20 记录，重跑迁移应能重建。
	if err := db.Migrator().DropTable(&model.BannerAnnouncement{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 20).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v19: %v", err)
	}
	if !db.Migrator().HasTable(&model.BannerAnnouncement{}) {
		t.Fatal("v20 upgrade did not reinstall banner_announcements table")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV21AddsBannerAnnouncementTitleRuns(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-banner-title-runs-v21?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.BannerAnnouncement{}, "title_runs") {
		t.Fatal("v21 migration did not add banner_announcements.title_runs")
	}
	legacy := &model.BannerAnnouncement{ID: "legacy-banner", Title: "旧库通知", Status: "active"}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.BannerAnnouncement{}, "title_runs"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 21).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v20: %v", err)
	}
	if !db.Migrator().HasColumn(&model.BannerAnnouncement{}, "title_runs") {
		t.Fatal("v21 upgrade did not restore banner_announcements.title_runs")
	}
	var stored model.BannerAnnouncement
	if err := db.First(&stored, "id = ?", "legacy-banner").Error; err != nil {
		t.Fatal(err)
	}
	if stored.Title != "旧库通知" {
		t.Fatalf("legacy banner lost during upgrade: %+v", stored)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV22AddsBannerAnnouncementNoticeType(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-banner-notice-type-v22?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.BannerAnnouncement{}, "notice_type") {
		t.Fatal("v22 migration did not add banner_announcements.notice_type")
	}
	legacy := &model.BannerAnnouncement{ID: "legacy-banner-v21", Title: "旧库通知", TitleRunsJSON: `[{"text":"旧库通知"}]`, Status: "active"}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.BannerAnnouncement{}, "notice_type"); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 22).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upgrade from v21: %v", err)
	}
	if !db.Migrator().HasColumn(&model.BannerAnnouncement{}, "notice_type") {
		t.Fatal("v22 upgrade did not restore banner_announcements.notice_type")
	}
	var stored model.BannerAnnouncement
	if err := db.First(&stored, "id = ?", "legacy-banner-v21").Error; err != nil {
		t.Fatal(err)
	}
	if stored.Title != "旧库通知" || stored.TitleRunsJSON != `[{"text":"旧库通知"}]` {
		t.Fatalf("legacy banner lost during upgrade: %+v", stored)
	}
	status, err := ReadSchemaStatus(db)
	if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected upgraded schema status: %+v, %v", status, err)
	}
}

func TestMigrateSchemaV23BackfillsCanvasRevisions(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-canvas-revisions-v23?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.CanvasSnapshotResource{}, &model.CanvasSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&model.CanvasProject{}, "Revision"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO canvas_projects (id, user_id, title, payload_json) VALUES ('legacy', 'owner', 'Existing canvas', '{"nodes":[]}')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 23).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	var project model.CanvasProject
	if err := db.First(&project, "id = ?", "legacy").Error; err != nil {
		t.Fatal(err)
	}
	if project.Revision != 1 || project.PayloadJSON != `{"nodes":[]}` {
		t.Fatalf("legacy canvas changed: %+v", project)
	}
	if !db.Migrator().HasTable(&model.CanvasSnapshot{}) || !db.Migrator().HasTable(&model.CanvasSnapshotResource{}) {
		t.Fatal("history tables missing")
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
}

func TestMigrateSchemaV8AllowsReusingArchivedLogicalModelCode(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-logical-model-active-code?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE logical_models (id text PRIMARY KEY, code text NOT NULL, archived_at datetime)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_logical_models_code ON logical_models(code)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO logical_models(id, code, archived_at) VALUES ('archived', 'gpt-image-2', CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateSchemaV8(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO logical_models(id, code, archived_at) VALUES ('active', 'gpt-image-2', NULL)`).Error; err != nil {
		t.Fatalf("reusing archived code after migration: %v", err)
	}
	if err := db.Exec(`INSERT INTO logical_models(id, code, archived_at) VALUES ('duplicate', 'gpt-image-2', NULL)`).Error; err == nil {
		t.Fatal("active logical model code must remain unique")
	}
}

func TestMigrateSchemaV12AddsAgentTokenChargeLimit(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-agent-token-charge-limit?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE billing_orders (id text PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateSchemaV12(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.BillingOrder{}, "ChargeLimitMicrocredits") {
		t.Fatal("migration v12 did not add Agent token charge limit")
	}
}

func TestMigrateSchemaRejectsChecksumMismatch(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-checksum?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&schemaMigration{}).Where("version = ?", CurrentSchemaVersion).Update("checksum", "changed").Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err == nil || !strings.Contains(err.Error(), "校验和不一致") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if err := RequireSchemaVersion(db); err == nil || !strings.Contains(err.Error(), "校验和不一致") {
		t.Fatalf("schema verification must reject checksum mismatch, got %v", err)
	}
}

func TestMigrateSchemaV3NormalizesLegacyAccessoryCategory(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-asset-taxonomy?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Asset{}, &model.ProjectAssetCandidate{}); err != nil {
		t.Fatal(err)
	}
	asset := model.Asset{ID: "asset-1", UserID: "user-1", Kind: "image", Category: model.AssetCategory("accessory"), Title: "旧配饰"}
	candidate := model.ProjectAssetCandidate{ID: "candidate-1", ProjectID: "project-1", Name: "旧配饰候选", Category: model.AssetCategory("accessory"), Status: "pending_confirmation"}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateSchemaV3(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&asset, "id = ?", asset.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&candidate, "id = ?", candidate.ID).Error; err != nil {
		t.Fatal(err)
	}
	if asset.Category != model.AssetCategoryProp || candidate.Category != model.AssetCategoryProp {
		t.Fatalf("legacy accessory categories = %q/%q, want prop/prop", asset.Category, candidate.Category)
	}
	if candidate.NameKey != model.AssetCandidateNameKey(candidate.Name) {
		t.Fatalf("candidate name key = %q", candidate.NameKey)
	}
}

func TestMigrateSchemaV4AddsResourceUploadKeyToExistingSchema(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-resource-upload-key?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE resources (id TEXT PRIMARY KEY, user_id TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ModelChannel{}, &model.ChannelModel{}, &model.ChannelModelPriceTier{}, &model.BillingOrder{}, &model.OAuthState{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range schemaMigrations[:3] {
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migrate existing schema: %v", err)
	}
	if !db.Migrator().HasColumn(&model.Resource{}, "upload_key") {
		t.Fatal("resource upload_key column was not added")
	}
	if !db.Migrator().HasIndex(&model.Resource{}, "idx_resources_user_upload_key") {
		t.Fatal("resource upload key index was not added")
	}
	var status SchemaStatus
	status, err = ReadSchemaStatus(db)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected schema status: %#v", status)
	}

	firstKey := "same-upload"
	if err := db.Exec(`INSERT INTO resources (id, user_id, upload_key) VALUES (?, ?, ?)`, "resource-1", "user-1", firstKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO resources (id, user_id, upload_key) VALUES (?, ?, ?)`, "resource-2", "user-1", firstKey).Error; err == nil {
		t.Fatal("duplicate resource upload key should be rejected")
	}
}

func TestMigrateSchemaRepairsLegacyAssetFoldersMigrationOrder(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-legacy-v6-order?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Resource{}, &model.Asset{}, &model.AssetFolder{}, &model.ModelChannel{}, &model.ChannelModel{}, &model.ChannelModelPriceTier{}, &model.BillingOrder{}, &model.OAuthState{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range schemaMigrations[:5] {
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&schemaMigration{Version: 6, Name: "asset_library_folders", Checksum: assetLibraryFoldersChecksum, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(db); err != nil {
		t.Fatalf("repair legacy migration order: %v", err)
	}
	if !db.Migrator().HasColumn(&model.Resource{}, "playback_status") || !db.Migrator().HasColumn(&model.Resource{}, "playback_object_key") || !db.Migrator().HasColumn(&model.Resource{}, "playback_error") {
		t.Fatal("legacy database did not receive resource playback columns")
	}
	status, err := ReadSchemaStatus(db)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Current != CurrentSchemaVersion {
		t.Fatalf("unexpected repaired schema status: %#v", status)
	}
	var applied schemaMigration
	if err := db.First(&applied, "version = ?", 6).Error; err != nil {
		t.Fatal(err)
	}
	if applied.Name != "asset_library_folders" || applied.Checksum != assetLibraryFoldersChecksum {
		t.Fatalf("historical migration 6 must be preserved: %#v", applied)
	}
	var playback schemaMigration
	if err := db.First(&playback, "version = ?", 7).Error; err != nil {
		t.Fatal(err)
	}
	if playback.Name != "resource_playback_variant" || playback.Checksum != resourcePlaybackChecksum {
		t.Fatalf("migration 7 must supply playback schema: %#v", playback)
	}
}

func TestMigrateSchemaRollsBackFailedMigration(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-rollback?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}

	original := schemaMigrations
	schemaMigrations = append(append([]migration(nil), original...), migration{
		version:  CurrentSchemaVersion + 1,
		name:     "rollback_probe",
		checksum: "sha256:rollback-probe",
		apply: func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TABLE migration_rollback_probe (id INTEGER PRIMARY KEY)").Error; err != nil {
				return err
			}
			return errors.New("forced migration failure")
		},
	})
	t.Cleanup(func() { schemaMigrations = original })

	if err := MigrateSchema(db); err == nil || !strings.Contains(err.Error(), "forced migration failure") {
		t.Fatalf("expected forced migration failure, got %v", err)
	}
	if db.Migrator().HasTable("migration_rollback_probe") {
		t.Fatal("failed migration left a partial table behind")
	}
	var count int64
	if err := db.Model(&schemaMigration{}).Where("version = ?", CurrentSchemaVersion+1).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration was recorded: %d", count)
	}
}

func TestRequireSchemaVersionRejectsUninitializedDatabase(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-uninitialized?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireSchemaVersion(db); err == nil || !strings.Contains(err.Error(), "请先执行 migrate-schema up") {
		t.Fatalf("expected missing migration error, got %v", err)
	}
}

func TestMigrateSchemaV13AddsCloudAgentCanvasMutation(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-cloud-agent-canvas-mutation?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&model.CloudAgentCanvasMutation{}) {
		t.Fatal("migration v13 did not create cloud agent canvas mutation table")
	}
	for _, field := range []string{"RunID", "BeforeSnapshotHash", "AfterSnapshotHash", "BeforeJSON", "HasSubmittedTask", "Status"} {
		if !db.Migrator().HasColumn(&model.CloudAgentCanvasMutation{}, field) {
			t.Fatalf("migration v13 did not add %s", field)
		}
	}
}

func TestMigrateSchemaV30AddsBuiltinTools(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-builtin-tools-v30?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&model.Tool{}) {
		t.Fatal("migration v30 did not create tools table")
	}
}

func TestMigrateSchemaV31AddsToolUserActions(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-tool-user-actions-v31?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&model.ToolFavorite{}) {
		t.Fatal("migration v31 did not create tool_favorites table")
	}
}

func TestMigrateSchemaAcceptsUpstreamTagsAtV32(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-upstream-tags-v32?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&schemaMigration{}).Where("version = ?", 32).Updates(map[string]any{
		"name":     "channel_model_tags",
		"checksum": legacyChannelModelTagsChecksum,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version = ?", 33).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("upstream v32 database should upgrade to local v33: %v", err)
	}
	var tagsMigration schemaMigration
	if err := db.First(&tagsMigration, "version = ?", 32).Error; err != nil {
		t.Fatal(err)
	}
	if tagsMigration.Name != "channel_model_tags" || tagsMigration.Checksum != legacyChannelModelTagsChecksum {
		t.Fatalf("upstream v32 migration history was rewritten: %#v", tagsMigration)
	}
	var localMigration schemaMigration
	if err := db.First(&localMigration, "version = ?", 33).Error; err != nil {
		t.Fatal(err)
	}
	if localMigration.Name != "channel_model_tags" || localMigration.Checksum != channelModelTagsChecksum {
		t.Fatalf("local v33 tags migration was not recorded: %#v", localMigration)
	}
	if !db.Migrator().HasColumn(&model.ChannelModel{}, "tags") {
		t.Fatal("channel model tags column missing after compatibility upgrade")
	}
}

func TestToolsUpgradeFromMain29PreservesMigrationChecksums(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:tools-main29-upgrade?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range schemaMigrations {
		if item.version > 29 {
			break
		}
		if err := item.apply(db); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The initial migration uses today's model registry; restore the actual v29
	// boundary so this test proves that v30/v31 create the new tables.
	if err := db.Migrator().DropTable(&model.ToolFavorite{}, &model.Tool{}); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(&model.Tool{}) || db.Migrator().HasTable(&model.ToolFavorite{}) {
		t.Fatal("tool tables must not exist before upgrading v29")
	}
	for range 2 {
		if err := MigrateSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []int64{28, 29} {
		var record schemaMigration
		if err := db.First(&record, version).Error; err != nil {
			t.Fatal(err)
		}
		expected := map[int64]string{28: "sha256:agent-execution-journal-v28", 29: "sha256:agent-resource-leases-v29-20260919"}
		if record.Checksum != expected[version] {
			t.Fatal("main checksum changed")
		}
	}
	if !db.Migrator().HasTable(&model.Tool{}) || !db.Migrator().HasTable(&model.ToolFavorite{}) {
		t.Fatal("tools tables missing")
	}
}
