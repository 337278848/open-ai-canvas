package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

func mergeMigrationFixture(t *testing.T, lineage string) *gorm.DB {
	t.Helper()
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatal(err)
	}

	var plan []migration
	for _, item := range schemaMigrations {
		limit := int64(52)
		if strings.HasPrefix(lineage, "upstream") {
			limit = 31
		} else if lineage == "local-v50" || lineage == "local-workbench-v51" {
			limit = 50
		}
		if item.version <= limit {
			plan = append(plan, item)
		}
	}
	if strings.HasPrefix(lineage, "upstream") {
		// Freeze the actual upstream identities rather than obtaining metadata
		// from the compatibility plan being tested. Upstream never recorded v44.
		for _, historical := range []struct {
			version, localVersion int64
			name, checksum        string
		}{
			{32, 33, "channel_model_tags", "sha256:channel-model-tags-v32"},
			{33, 37, "oauth_state_accepted_terms", "sha256:oauth-state-accepted-terms-v33"},
			{34, 38, "task_media_recovery", "sha256:task-media-recovery-v34"},
			{35, 39, "auth_notifications", "sha256:auth-notifications-v35-20260924"},
			{36, 40, "cloud_agent_gemini_cache", "sha256:cloud-agent-gemini-cache-v36-20260924"},
			{37, 41, "cloud_agent_gemini_cache_identity", "sha256:cloud-agent-gemini-cache-identity-v37-20260925"},
			{38, 42, "prefixed_id_sequence_reconcile", "sha256:prefixed-id-sequence-reconcile-v38-20260926"},
			{39, 43, "skill_library_categories", "sha256:skill-library-categories-v39-20260926"},
			{40, 44, "builtin_skill_tombstones", "sha256:builtin-skill-tombstones-v40-20260927"},
			{41, 45, "resource_thumbnail", "sha256:resource-thumbnail-v41-20260927"},
			{42, 46, "cloud_agent_pi_sessions", "sha256:cloud-agent-pi-sessions-v42-20260928"},
			{43, 47, "topup_sale_strategies", "sha256:topup-sale-strategies-v43-20260929"},
			{45, 48, "upload_reservations", "sha256:upload-reservations-v45-20261005"},
			{46, 49, "skill_curation", "sha256:skill-curation-v46-20261005"},
			{47, 50, "skill_curation_roots", "sha256:skill-curation-roots-v47-20261005"},
			{48, 51, "channel_proxy_url", "sha256:channel-proxy-url-v48-20261009"},
			{49, 52, "banner_sort_order", "sha256:banner-sort-order-v49-20261010"},
			{50, 53, "canvas_mutation_receipts", "sha256:canvas-mutation-receipts-v50-20261011"},
			{51, 54, "personal_profile_security", "sha256:personal-profile-security-v51-20261012"},
			{52, 55, "user_avatar_presets", "sha256:user-avatar-presets-v52-20261012"},
			{53, 56, "user_login_logs", "sha256:user-login-logs-v53"},
			{54, 57, "workbench_tools_and_presets", "sha256:workbench-tools-presets-v54-20261014"},
		} {
			if lineage == "upstream-workbench-v51" && historical.version > 50 {
				break
			}
			item, ok := migrationByVersion(schemaMigrations, historical.localVersion)
			if !ok {
				t.Fatalf("fixture missing local migration %d", historical.localVersion)
			}
			item.version, item.name, item.checksum = historical.version, historical.name, historical.checksum
			if lineage == "upstream-mixed-suffix" && (historical.version == 43 || historical.version == 50 || historical.version == 51) {
				// A partially merged build recorded local work in these slots;
				// upstream v53/v54 still occupy the new receipt/profile slots.
				item, ok = migrationByVersion(schemaMigrations, historical.version)
				if !ok {
					t.Fatalf("fixture missing mixed migration %d", historical.version)
				}
			}
			plan = append(plan, item)
		}
	}
	if strings.HasSuffix(lineage, "workbench-v51") {
		item, ok := migrationByVersion(schemaMigrations, workbenchToolsMigrationVersion)
		if !ok {
			t.Fatal("fixture missing workbench migration")
		}
		item.version, item.checksum = 51, "sha256:workbench-tools-presets-v51-20261014"
		plan = append(plan, item)
	}
	for _, item := range plan {
		if err := item.apply(db); err != nil {
			t.Fatalf("fixture migration %d (%s): %v", item.version, item.name, err)
		}
		if err := db.Create(&schemaMigration{
			Version: item.version, Name: item.name, Checksum: item.checksum,
			AppliedAt: time.Date(2026, 10, 12, 0, 0, int(item.version), 0, time.UTC),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Baseline AutoMigrate uses today's Models. Remove the future schema to
	// exercise a real upgrade, not success caused by a freshly-created baseline.
	if lineage != "upstream-v54" {
		tables := []any{&model.UserAvatar{}}
		if lineage != "upstream-mixed-suffix" {
			tables = append(tables, &model.LoginLog{})
		}
		for _, table := range tables {
			if err := db.Migrator().DropTable(table); err != nil {
				t.Fatal(err)
			}
		}
		if lineage != "upstream-workbench-v51" {
			if err := db.Migrator().DropTable(&model.CanvasMutationReceipt{}); err != nil {
				t.Fatal(err)
			}
		}
		for _, fields := range []struct {
			model   any
			columns []string
		}{
			{&model.User{}, []string{"AvatarMode", "AvatarVersion", "AvatarPreset"}},
			{&model.AuthSession{}, []string{"ReauthenticatedAt"}},
			{&model.OAuthState{}, []string{"ReauthUserID", "ReauthSessionID"}},
			{&model.AuthVerification{}, []string{"SessionID", "CredentialHash"}},
		} {
			for _, column := range fields.columns {
				if lineage == "upstream-mixed-suffix" && column == "AvatarPreset" {
					continue
				}
				if err := db.Migrator().DropColumn(fields.model, column); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if !strings.HasSuffix(lineage, "workbench-v51") {
		if lineage != "upstream-v54" && lineage != "upstream-mixed-suffix" {
			for _, table := range []any{&model.WorkbenchTool{}, &model.WorkbenchPreset{}} {
				if err := db.Migrator().DropTable(table); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Migrator().DropIndex(&model.Task{}, "idx_tasks_workbench_kind"); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().DropColumn(&model.Task{}, "WorkbenchKind"); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		if err := db.Create(&model.WorkbenchTool{ID: "old-tool", Name: "keep tool", Kind: "image", Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.WorkbenchPreset{ID: "old-preset", ToolID: "old-tool", Name: "keep preset", PromptTemplate: "keep prompt"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if lineage == "local-v50" || lineage == "local-workbench-v51" {
		if err := db.Migrator().DropColumn(&model.ModelChannel{}, "ProxyURL"); err != nil {
			t.Fatal(err)
		}
	}
	if lineage == "local-v50" {
		if err := db.Migrator().DropColumn(&model.BannerAnnouncement{}, "SortOrder"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("INSERT INTO users (id, username, email, password_hash) VALUES ('keep-user', 'keep-user', 'keep@example.test', 'keep-password-hash')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO auth_sessions (id, user_id, token_hash) VALUES ('keep-session', 'keep-user', 'keep-token-hash')").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func mergeMigrationHistory(t *testing.T, db *gorm.DB) []schemaMigration {
	t.Helper()
	var history []schemaMigration
	if err := db.Order("version ASC").Find(&history).Error; err != nil {
		t.Fatal(err)
	}
	return history
}

func assertMergeHistoryPreserved(t *testing.T, db *gorm.DB, before []schemaMigration) {
	t.Helper()
	for _, record := range before {
		var after schemaMigration
		if err := db.First(&after, "version = ?", record.Version).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, after) {
			t.Fatalf("rewrote historical migration: before=%+v after=%+v", record, after)
		}
	}
}

func mergeSchemaSnapshot(t *testing.T, db *gorm.DB) []struct{ Type, Name, SQL string } {
	t.Helper()
	var snapshot []struct{ Type, Name, SQL string }
	if err := db.Raw("SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name").Scan(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestMigrateSchemaMergePreservesPublishedHistory(t *testing.T) {
	for _, lineage := range []string{"local-v50", "local-v52", "local-workbench-v51", "upstream-workbench-v51", "upstream-v54", "upstream-mixed-suffix"} {
		t.Run(lineage, func(t *testing.T) {
			db := mergeMigrationFixture(t, lineage)
			before := mergeMigrationHistory(t, db)
			for attempt := 0; attempt < 2; attempt++ {
				if err := MigrateSchema(db); err != nil {
					t.Fatalf("upgrade %s attempt %d: %v", lineage, attempt, err)
				}
				assertMergeHistoryPreserved(t, db, before)
				before = mergeMigrationHistory(t, db)
			}
			for _, table := range []any{&model.CanvasMutationReceipt{}, &model.UserAvatar{}, &model.LoginLog{}, &model.WorkbenchTool{}, &model.WorkbenchPreset{}, &model.CloudAgentPiSession{}, &model.UploadReservation{}, &model.SkillLibraryCategory{}, &model.SkillCurationSetting{}, &model.SkillCurationRoot{}} {
				if !db.Migrator().HasTable(table) {
					t.Fatalf("upgrade missing table %T", table)
				}
			}
			for _, field := range []struct {
				model  any
				column string
			}{
				{&model.User{}, "AvatarMode"}, {&model.User{}, "AvatarVersion"}, {&model.User{}, "AvatarPreset"},
				{&model.AuthSession{}, "ReauthenticatedAt"}, {&model.OAuthState{}, "ReauthUserID"}, {&model.OAuthState{}, "ReauthSessionID"},
				{&model.AuthVerification{}, "SessionID"}, {&model.AuthVerification{}, "CredentialHash"},
				{&model.ModelChannel{}, "ProxyURL"}, {&model.BannerAnnouncement{}, "SortOrder"}, {&model.Task{}, "WorkbenchKind"},
			} {
				if !db.Migrator().HasColumn(field.model, field.column) {
					t.Fatalf("upgrade missing %T.%s", field.model, field.column)
				}
			}
			for _, index := range []struct {
				model any
				name  string
			}{
				{&model.WorkbenchTool{}, "idx_workbench_tools_kind_enabled_sort"},
				{&model.WorkbenchPreset{}, "idx_workbench_presets_tool_enabled_sort"},
				{&model.Task{}, "idx_tasks_workbench_kind"},
			} {
				if !db.Migrator().HasIndex(index.model, index.name) {
					t.Fatalf("upgrade missing index %s", index.name)
				}
			}
			var user model.User
			var session model.AuthSession
			if err := db.First(&user, "id = ?", "keep-user").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&session, "id = ?", "keep-session").Error; err != nil {
				t.Fatal(err)
			}
			if user.Email != "keep@example.test" || user.PasswordHash != "keep-password-hash" || session.TokenHash != "keep-token-hash" || session.ReauthenticatedAt != nil {
				t.Fatal("upgrade changed credentials or reauthentication state")
			}
			if strings.HasSuffix(lineage, "workbench-v51") {
				var tool model.WorkbenchTool
				var preset model.WorkbenchPreset
				if err := db.First(&tool, "id = ?", "old-tool").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.First(&preset, "id = ?", "old-preset").Error; err != nil {
					t.Fatal(err)
				}
				if tool.Name != "keep tool" || !tool.Enabled || preset.PromptTemplate != "keep prompt" {
					t.Fatal("upgrade changed legacy workbench content")
				}
			}

			// Read-only readiness validates both historical identities without
			// running convergence, even when a physical feature is missing.
			if err := db.Migrator().DropTable(&model.CanvasMutationReceipt{}); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().DropColumn(&model.ModelChannel{}, "ProxyURL"); err != nil {
				t.Fatal(err)
			}
			schemaBefore := mergeSchemaSnapshot(t, db)
			status, err := ReadSchemaStatus(db)
			if err != nil || !status.Ready || status.Current != CurrentSchemaVersion {
				t.Fatalf("merged schema status: %+v, %v", status, err)
			}
			if err := RequireSchemaVersion(db); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(schemaBefore, mergeSchemaSnapshot(t, db)) {
				t.Fatal("read-only validation repaired physical schema")
			}
			assertMergeHistoryPreserved(t, db, before)
		})
	}
}

func TestMigrateSchemaMergeRejectsInvalidLegacyWorkbenchAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"checksum", "missing-table", "missing-column", "missing-index", "invalid-later-row", "failed-new-migration"} {
		t.Run(scenario, func(t *testing.T) {
			db := mergeMigrationFixture(t, "upstream-workbench-v51")
			wantError := ""
			switch scenario {
			case "checksum":
				wantError = "校验和不一致"
				if err := db.Model(&schemaMigration{}).Where("version = ?", 51).Update("checksum", "tampered").Error; err != nil {
					t.Fatal(err)
				}
			case "missing-table":
				wantError = "工作台表不完整"
				if err := db.Migrator().DropTable(&model.WorkbenchPreset{}); err != nil {
					t.Fatal(err)
				}
			case "missing-column":
				wantError = "旧工作台结构不完整"
				if err := db.Migrator().DropColumn(&model.WorkbenchPreset{}, "PromptTemplate"); err != nil {
					t.Fatal(err)
				}
			case "missing-index":
				wantError = "旧工作台结构不完整"
				if err := db.Migrator().DropIndex(&model.Task{}, "idx_tasks_workbench_kind"); err != nil {
					t.Fatal(err)
				}
			case "invalid-later-row":
				wantError = "历史迁移 52 不受支持"
				if err := db.Create(&schemaMigration{Version: 52, Name: "unknown", Checksum: "unknown", AppliedAt: time.Now().UTC()}).Error; err != nil {
					t.Fatal(err)
				}
			case "failed-new-migration":
				wantError = "forced merged migration failure"
				original := schemaMigrations
				schemaMigrations = append([]migration(nil), original...)
				item, ok := migrationByVersion(schemaMigrations, workbenchToolsMigrationVersion)
				if !ok {
					t.Fatal("missing workbench migration")
				}
				item.apply = func(tx *gorm.DB) error {
					if err := tx.Exec("CREATE TABLE merge_failure_probe (id INTEGER PRIMARY KEY)").Error; err != nil {
						return err
					}
					return errors.New(wantError)
				}
				schemaMigrations = replaceMigration(schemaMigrations, item)
				t.Cleanup(func() { schemaMigrations = original })
			}
			before := mergeMigrationHistory(t, db)
			schemaBefore := mergeSchemaSnapshot(t, db)
			if err := MigrateSchema(db); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("expected %q, got %v", wantError, err)
			}
			if !reflect.DeepEqual(before, mergeMigrationHistory(t, db)) {
				t.Fatal("failed upgrade changed migration history")
			}
			if !reflect.DeepEqual(schemaBefore, mergeSchemaSnapshot(t, db)) {
				t.Fatal("failed upgrade left partial schema changes")
			}
		})
	}
}

func TestSchemaMigrationMergeSuffixAppendsUpstreamFeatures(t *testing.T) {
	if len(schemaMigrations) != 61 || CurrentSchemaVersion != 61 {
		t.Fatalf("merged plan must append to v61: count=%d current=%d", len(schemaMigrations), CurrentSchemaVersion)
	}
	for _, expected := range []struct {
		version        int64
		name, checksum string
	}{
		{53, "canvas_mutation_receipts", "sha256:canvas-mutation-receipts-v53-20261011-local-lineage"},
		{54, "personal_profile_security", "sha256:personal-profile-security-v54-20261012-local-lineage"},
		{55, "user_avatar_presets", "sha256:user-avatar-presets-v55-20261012-local-lineage"},
		{56, "user_login_logs", "sha256:user-login-logs-v56-local-lineage"},
		{57, "workbench_tools_and_presets", "sha256:workbench-tools-presets-v57-20261013-local-lineage"},
		{58, "lxmone_h3c_generations_protocol", lxmoneH3CProtocolChecksum},
		{59, "provider_catalog_source_merge", "sha256:provider-catalog-source-v59-20261017"},
		{60, "sql_perf_indexes_merge", "sha256:sql-perf-indexes-v60-20261017"},
		{61, "canvas_multi_session_sync_merge", "sha256:canvas-multi-session-sync-v61-20261017"},
	} {
		item, ok := migrationByVersion(schemaMigrations, expected.version)
		if !ok || item.name != expected.name || item.checksum != expected.checksum || item.apply == nil {
			t.Fatalf("unexpected appended migration v%d: %+v", expected.version, item)
		}
	}
}
