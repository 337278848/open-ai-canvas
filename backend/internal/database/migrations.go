package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const CurrentSchemaVersion int64 = 36

const baselineSchemaChecksum = "sha256:open-ai-canvas-schema-v1-20260830"
const schemaMigrationAppliedAtIndexChecksum = "sha256:schema-migrations-applied-at-index-v2-20260830"
const assetTaxonomyCandidateIdentityChecksum = "sha256:asset-taxonomy-candidate-identity-v3-20260831-r1"
const resourceUploadKeyChecksum = "sha256:resource-upload-key-v4-20260901"
const paymentTopupChecksum = "sha256:payment-topup-v5-20260902"
const resourcePlaybackChecksum = "sha256:resource-playback-v6-20260902"
const assetLibraryFoldersChecksum = "sha256:asset-library-folders-v6-20260902"
const logicalModelActiveCodeChecksum = "sha256:logical-model-active-code-v8-20260905"
const creationRuntimeChecksum = "sha256:creation-runtime-v10-20260909"
const resourceUpstreamRelayChecksum = "sha256:resource-upstream-relay-v28-20260919"
const legacyResourceUpstreamRelayChecksum = "sha256:resource-upstream-relay-v16-20260915"
const resourceUpstreamRelayReconciliationChecksum = "sha256:resource-upstream-relay-v28-reconciliation-v30-20260920"
const toolSchemaReconciliationChecksum = "sha256:tool-schema-reconciliation-v32-20260920"
const channelModelTagsChecksum = "sha256:channel-model-tags-v33-20260921"
const legacyChannelModelTagsChecksum = "sha256:channel-model-tags-v32"
const lxmoneH3CapabilityChecksum = "sha256:lxmone-h3-capability-v34-20260922"
const providerRequestIDReconciliationChecksum = "sha256:provider-request-id-reconciliation-v35-20260922"
const reconciliationFollowupChecksum = "sha256:capability-provider-reconciliation-followup-v36-20260922"

const postgresSchemaMigrationLockID int64 = 73123910420260830

type SchemaStatus struct {
	Current  int64 `json:"current"`
	Expected int64 `json:"expected"`
	Ready    bool  `json:"ready"`
}

type schemaMigration struct {
	Version   int64     `gorm:"primaryKey"`
	Name      string    `gorm:"size:160;not null"`
	Checksum  string    `gorm:"size:96;not null"`
	AppliedAt time.Time `gorm:"not null"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

type migration struct {
	version  int64
	name     string
	checksum string
	apply    func(*gorm.DB) error
}

var schemaMigrations = []migration{
	{version: 1, name: "baseline_gorm_schema", checksum: baselineSchemaChecksum, apply: migrateSchemaV1},
	{version: 2, name: "schema_migrations_applied_at_index", checksum: schemaMigrationAppliedAtIndexChecksum, apply: migrateSchemaV2},
	{version: 3, name: "asset_taxonomy_candidate_identity", checksum: assetTaxonomyCandidateIdentityChecksum, apply: migrateSchemaV3},
	{version: 4, name: "resource_upload_key", checksum: resourceUploadKeyChecksum, apply: migrateSchemaV4},
	{version: 5, name: "payment_topup", checksum: paymentTopupChecksum, apply: migrateSchemaV5},
	{version: 6, name: "resource_playback_variant", checksum: resourcePlaybackChecksum, apply: migrateSchemaV6},
	{version: 7, name: "asset_library_folders", checksum: assetLibraryFoldersChecksum, apply: migrateSchemaV7},
	{version: 8, name: "logical_model_active_code", checksum: logicalModelActiveCodeChecksum, apply: migrateSchemaV8},
	{version: 9, name: "channel_presentation", checksum: "sha256:channel-presentation-v9-20260908", apply: migrateChannelPresentation},
	{version: 10, name: "creation_runtime", checksum: creationRuntimeChecksum, apply: migrateSchemaV10},
	{version: 11, name: "cloud_agent_runtime", checksum: "sha256:cloud-agent-runtime-v11-20260912", apply: func(tx *gorm.DB) error { return tx.AutoMigrate(&model.CloudAgentExecution{}) }},
	{version: 12, name: "agent_token_charge_limit", checksum: "sha256:agent-token-charge-limit-v12-20260913", apply: migrateSchemaV12},
	{version: 13, name: "cloud_agent_canvas_mutation", checksum: "sha256:cloud-agent-canvas-mutation-v13-20260913", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.CloudAgentCanvasMutation{})
	}},
	{version: 14, name: "cloud_agent_recovery_control", checksum: "sha256:cloud-agent-recovery-control-v14", apply: migrateSchemaV14},
	{version: 15, name: "agent_profiles", checksum: "sha256:agent-profiles-v15-20260914", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.AgentProfile{})
	}},
	{version: 16, name: "agent_lessons", checksum: "sha256:agent-lessons-v16-20260917", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.AgentLesson{})
	}},
	{version: 17, name: "agent_lessons_owner_index", checksum: "sha256:agent-lessons-owner-index-v17-20260917", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.AgentLesson{})
	}},
	{version: 18, name: "agent_memory_settings", checksum: "sha256:agent-memory-settings-v18-20260917", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.AgentMemorySetting{})
	}},
	{version: 19, name: "payment_plugin_version", checksum: "sha256:payment-plugin-version-v19-20260917", apply: migrateSchemaV19},
	{version: 20, name: "banner_announcements", checksum: "sha256:banner-announcements-v20-20260917", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.BannerAnnouncement{})
	}},
	{version: 21, name: "banner_announcement_title_runs", checksum: "sha256:banner-announcement-title-runs-v21-20260917", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.BannerAnnouncement{})
	}},
	{version: 22, name: "banner_announcement_notice_type", checksum: "sha256:banner-announcement-notice-type-v22-20260917", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.BannerAnnouncement{})
	}},
	{version: 23, name: "canvas_revision_history", checksum: "sha256:canvas-revision-history-v23-20260918", apply: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&model.CanvasProject{}, &model.CanvasSnapshot{}, &model.CanvasSnapshotResource{})
	}},
	{version: 24, name: "channel_model_label", checksum: "sha256:channel-model-label-v24", apply: migrateChannelModelLabel},
	{version: 25, name: "video_token_formula_snapshot", checksum: "sha256:video-token-formula-snapshot-v25", apply: migrateVideoTokenFormulaSnapshot},
	{version: 26, name: "channel_model_description", checksum: "sha256:channel-model-description-v26", apply: migrateChannelModelDescription},
	{version: 27, name: "channel_credit_cost", checksum: "sha256:channel-credit-cost-v27", apply: migrateChannelCreditCost},
	{version: 28, name: "agent_execution_journal", checksum: "sha256:agent-execution-journal-v28", apply: migrateAgentExecutionJournal},
	{version: 29, name: "agent_resource_leases", checksum: "sha256:agent-resource-leases-v29-20260919", apply: migrateAgentResourceLeases},
	{version: 30, name: "builtin_tools", checksum: "sha256:builtin-tools-v30", apply: migrateBuiltinTools},
	{version: 31, name: "tool_favorites", checksum: "sha256:tool-favorites-v31", apply: migrateToolFavorites},
	{version: 32, name: "tool_schema_reconciliation", checksum: toolSchemaReconciliationChecksum, apply: migrateToolSchemaReconciliation},
	{version: 33, name: "channel_model_tags", checksum: channelModelTagsChecksum, apply: migrateChannelModelTags},
	{version: 34, name: "lxmone_h3_capability_limits", checksum: lxmoneH3CapabilityChecksum, apply: migrateLXMoneH3CapabilityLimits},
	{version: 35, name: "provider_request_id_reconciliation", checksum: providerRequestIDReconciliationChecksum, apply: migrateFabricatedProviderRequestIDs},
	{version: 36, name: "capability_provider_reconciliation_followup", checksum: reconciliationFollowupChecksum, apply: migrateCapabilityProviderReconciliationFollowup},
}

func migrateAgentExecutionJournal(tx *gorm.DB) error {
	return tx.AutoMigrate(&model.CloudAgentExecution{}, &model.CloudAgentEventRecord{}, &model.CloudAgentMessageRecord{}, &model.Task{}, &model.BillingOrder{})
}

func migrateAgentResourceLeases(tx *gorm.DB) error {
	return tx.AutoMigrate(&model.CloudAgentResourceLease{})
}

// migrateResourceUpstreamRelay adds the local upstream-media relay cache.
// It is intentionally idempotent because the same physical schema was shipped
// under a conflicting local v28 (and an older local v16) before upstream used
// v28 for the Agent execution journal.
func migrateResourceUpstreamRelay(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.Resource{}) {
		return fmt.Errorf("资源表不存在")
	}
	for _, column := range []struct {
		field string
		label string
	}{{field: "RelayURL", label: "图床中继地址"}, {field: "RelayExpiresAt", label: "图床中继过期时间"}} {
		if tx.Migrator().HasColumn(&model.Resource{}, column.field) {
			continue
		}
		if err := tx.Migrator().AddColumn(&model.Resource{}, column.field); err != nil {
			return fmt.Errorf("增加资源%s列：%w", column.label, err)
		}
	}
	return nil
}

// v30 is the convergence point for both historical v28 lineages. Official
// databases already have the Agent journal; local databases already have the
// relay columns. Re-running all three idempotent schema operations makes both
// lineages physically complete without rewriting historical migration records.
func migrateSchemaV30Reconciliation(tx *gorm.DB) error {
	if err := migrateAgentExecutionJournal(tx); err != nil {
		return err
	}
	if err := migrateAgentResourceLeases(tx); err != nil {
		return err
	}
	return migrateResourceUpstreamRelay(tx)
}

func migrateBuiltinTools(tx *gorm.DB) error {
	if err := migrateResourceUpstreamRelay(tx); err != nil {
		return err
	}
	return tx.AutoMigrate(&model.Tool{})
}

func migrateToolFavorites(tx *gorm.DB) error {
	return tx.AutoMigrate(&model.ToolFavorite{})
}

// v32 makes both the official tools lineage and the historical local relay
// lineages physically complete. It is deliberately idempotent.
func migrateToolSchemaReconciliation(tx *gorm.DB) error {
	if err := migrateAgentExecutionJournal(tx); err != nil {
		return err
	}
	if err := migrateAgentResourceLeases(tx); err != nil {
		return err
	}
	if err := migrateResourceUpstreamRelay(tx); err != nil {
		return err
	}
	if err := migrateBuiltinTools(tx); err != nil {
		return err
	}
	return migrateToolFavorites(tx)
}

func migrateChannelModelTags(tx *gorm.DB) error {
	if tx.Migrator().HasColumn(&model.ChannelModel{}, "Tags") {
		return nil
	}
	return tx.Migrator().AddColumn(&model.ChannelModel{}, "Tags")
}

func migrateLXMoneH3CapabilityLimits(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ChannelModel{}) {
		return nil
	}
	var items []model.ChannelModel
	if err := tx.Where("capability = ? AND protocol = ?", "video", "lxmone-h3-workflow").Find(&items).Error; err != nil {
		return fmt.Errorf("读取 LXMone H3 渠道模型：%w", err)
	}
	for _, item := range items {
		modelName := strings.ToLower(strings.TrimSpace(item.ProviderModelKey))
		if modelName == "" {
			modelName = strings.ToLower(strings.TrimSpace(item.ModelKey))
		}
		modelName = strings.TrimPrefix(modelName, "models/")
		updated, changed := migrateLXMoneH3CapabilityJSON(item.CapabilityConfigJSON, modelName)
		if !changed {
			continue
		}
		if err := tx.Model(&model.ChannelModel{}).Where("id = ?", item.ID).Updates(map[string]any{
			"capability_config_json": updated,
			"capability_version":     gorm.Expr("capability_version + ?", 1),
		}).Error; err != nil {
			return fmt.Errorf("回填 LXMone H3 模型能力 %s：%w", item.ID, err)
		}
	}
	return nil
}

func migrateLXMoneH3CapabilityJSON(raw string, modelName string) (string, bool) {
	if strings.TrimSpace(raw) == "" || (modelName != "minimax-h3-a" && modelName != "minimax-h3-e") {
		return raw, false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		// 损坏的能力配置由现有读路径隔离；迁移不能因为一条坏记录阻断整个数据库升级。
		return raw, false
	}
	video, ok := root["video"].(map[string]any)
	if !ok {
		return raw, false
	}
	changed := false
	if modelName == "minimax-h3-a" {
		if duration, ok := video["duration"].(map[string]any); ok {
			switch strings.ToLower(strings.TrimSpace(fmt.Sprint(duration["selection"]))) {
			case "range":
				limit := 12
				if maximum, ok := migrationJSONInt(duration["max"]); ok {
					if maximum > limit {
						duration["max"] = limit
						maximum = limit
						changed = true
					}
					if defaultValue, ok := migrationJSONInt(duration["default"]); ok && defaultValue > maximum {
						duration["default"] = maximum
						changed = true
					}
				} else if defaultValue, ok := migrationJSONInt(duration["default"]); ok && defaultValue > limit {
					duration["default"] = limit
					changed = true
				}
			case "enum":
				if values, ok := duration["values"].([]any); ok {
					filtered := make([]any, 0, len(values))
					for _, value := range values {
						seconds, valid := migrationJSONInt(value)
						if valid && seconds <= 12 {
							filtered = append(filtered, value)
						}
					}
					if len(filtered) != len(values) {
						duration["values"] = filtered
						changed = true
					}
					if len(filtered) > 0 {
						defaultValue, _ := migrationJSONInt(duration["default"])
						found := false
						for _, value := range filtered {
							if seconds, valid := migrationJSONInt(value); valid && seconds == defaultValue {
								found = true
								break
							}
						}
						if !found {
							duration["default"] = filtered[len(filtered)-1]
							changed = true
						}
					}
				}
			}
		}
	} else if references, ok := video["references"].(map[string]any); ok {
		if maximum, ok := migrationJSONInt(references["maxImages"]); ok && maximum > 1 {
			references["maxImages"] = 1
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return raw, false
	}
	return string(encoded), true
}

func migrationJSONInt(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), value == float64(int(value))
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

// migrateFabricatedProviderRequestIDs repairs records written by the old
// collection-path fallback. A collection endpoint such as
// /v1/videos/generations is not a provider task ID. Repair every create log,
// not only failed logs: a successful HTTP response can still lack a task ID
// and later fail in the response parser after the log has already been saved.
func migrateFabricatedProviderRequestIDs(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ApiCallLog{}) {
		return nil
	}
	var logs []model.ApiCallLog
	// The old fallback could only manufacture the literal segment
	// "generations". Restrict the migration query to that sentinel so startup
	// does not load every historical successful create log into memory.
	if err := tx.Where("request_kind = ? AND provider_request_id = ?", "create", "generations").Find(&logs).Error; err != nil {
		return fmt.Errorf("读取待修复 Provider ID 日志：%w", err)
	}
	for _, item := range logs {
		fabricatedID := fabricatedProviderRequestIDFromPath(item.Path)
		if fabricatedID == "" || strings.TrimSpace(item.ProviderRequestID) != fabricatedID {
			continue
		}
		replacement := migrationProviderResponseID(item.ResponseBody)
		if replacement == fabricatedID {
			continue
		}
		if err := tx.Model(&model.ApiCallLog{}).Where("id = ? AND provider_request_id = ?", item.ID, fabricatedID).Update("provider_request_id", replacement).Error; err != nil {
			return fmt.Errorf("清理 API 调用日志 Provider ID %s：%w", item.ID, err)
		}
		if err := reconcileFabricatedProviderRequestID(tx, item, fabricatedID, replacement); err != nil {
			return err
		}
	}
	return nil
}

// migrateCapabilityProviderReconciliationFollowup is intentionally idempotent.
// v34/v35 may already have been applied by an earlier build, so the follow-up
// re-runs both data repairs for databases that recorded those versions before
// the edge-case fixes landed.
func migrateCapabilityProviderReconciliationFollowup(tx *gorm.DB) error {
	if err := migrateLXMoneH3CapabilityLimits(tx); err != nil {
		return err
	}
	return migrateFabricatedProviderRequestIDs(tx)
}

func fabricatedProviderRequestIDFromPath(path string) string {
	if parsed, err := url.Parse(strings.TrimSpace(path)); err == nil && parsed.Path != "" {
		path = parsed.Path
	}
	parts := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	last := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
	parent := strings.ToLower(strings.TrimSpace(parts[len(parts)-2]))
	if last == "generations" && (parent == "video" || parent == "videos" || parent == "images") {
		return "generations"
	}
	return ""
}

func migrationProviderResponseID(raw string) string {
	if payload, ok := migrationDecodeJSON([]byte(raw)); ok {
		if value := migrationProviderResponseIDValue(payload); value != "" {
			return value
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		if payload, ok := migrationDecodeJSON([]byte(line)); ok {
			if value := migrationProviderResponseIDValue(payload); value != "" {
				return value
			}
		}
	}
	return ""
}

func migrationDecodeJSON(raw []byte) (any, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, false
	}
	return payload, true
}

func migrationProviderResponseIDValue(value any) string {
	// Search task-specific fields across the whole response before generic
	// wrapper/correlation fields. A response may contain both
	// {"id":"request-wrapper","data":{"task_id":"actual-task"}}.
	if candidate := migrationProviderResponseIDByKeys(value, "task_id", "taskId"); candidate != "" {
		return candidate
	}
	return migrationProviderResponseIDByKeys(value, "id", "request_id", "name")
}

func migrationProviderResponseIDByKeys(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if candidate, ok := typed[key].(string); ok && strings.TrimSpace(candidate) != "" {
				return strings.TrimSpace(candidate)
			}
		}
		for _, key := range []string{"data", "task", "response", "result", "output"} {
			if nested, ok := typed[key]; ok {
				if candidate := migrationProviderResponseIDByKeys(nested, keys...); candidate != "" {
					return candidate
				}
			}
		}
	case []any:
		for _, nested := range typed {
			if candidate := migrationProviderResponseIDByKeys(nested, keys...); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func reconcileFabricatedProviderRequestID(tx *gorm.DB, item model.ApiCallLog, fabricatedID string, replacement string) error {
	taskTargetID := strings.TrimSpace(replacement)
	if item.TaskID != "" && tx.Migrator().HasTable(&model.Task{}) {
		var task model.Task
		if err := tx.First(&task, "id = ?", item.TaskID).Error; err == nil && strings.TrimSpace(task.ProviderRequestID) == fabricatedID {
			if taskTargetID == "" {
				taskTargetID = migrationOtherProviderRequestIDForTask(tx, item, fabricatedID)
			}
			updates := map[string]any{"provider_request_id": taskTargetID, "updated_at": time.Now()}
			if taskTargetID == "" {
				updates["poll_stage"] = ""
				updates["next_poll_at"] = nil
			}
			if err := tx.Model(&model.Task{}).Where("id = ? AND provider_request_id = ?", item.TaskID, fabricatedID).Updates(updates).Error; err != nil {
				return fmt.Errorf("清理任务 Provider ID %s：%w", item.TaskID, err)
			}
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("读取任务 Provider ID %s：%w", item.TaskID, err)
		}
	}
	billingTargetID := strings.TrimSpace(replacement)
	if item.BillingOrderID != "" && tx.Migrator().HasTable(&model.BillingOrder{}) {
		if billingTargetID == "" {
			billingTargetID = migrationOtherProviderRequestIDForBilling(tx, item, fabricatedID)
		}
		if err := tx.Model(&model.BillingOrder{}).Where("id = ? AND provider_request_id = ?", item.BillingOrderID, fabricatedID).Update("provider_request_id", billingTargetID).Error; err != nil {
			return fmt.Errorf("清理账单 Provider ID %s：%w", item.BillingOrderID, err)
		}
	}
	if item.TaskID != "" && tx.Migrator().HasTable(&model.RouteAttempt{}) {
		attemptTargetID := taskTargetID
		if attemptTargetID == "" {
			attemptTargetID = billingTargetID
		}
		attemptUpdates := map[string]any{"provider_request_id": attemptTargetID}
		if attemptTargetID == "" {
			attemptUpdates["dispatch_state"] = "submission_unknown"
		} else {
			attemptUpdates["dispatch_state"] = "accepted"
		}
		attemptQuery := tx.Model(&model.RouteAttempt{}).Where("task_id = ? AND provider_request_id = ?", item.TaskID, fabricatedID)
		if attemptTargetID == "" {
			// A synchronous request may have completed successfully without
			// returning a task ID. Do not rewrite an already successful route
			// attempt to submission_unknown just because its old ID was fake.
			if err := attemptQuery.Where("status <> ?", "succeeded").Updates(attemptUpdates).Error; err != nil {
				return fmt.Errorf("清理路由尝试 Provider ID %s：%w", item.TaskID, err)
			}
			if err := tx.Model(&model.RouteAttempt{}).Where("task_id = ? AND provider_request_id = ? AND status = ?", item.TaskID, fabricatedID, "succeeded").Update("provider_request_id", "").Error; err != nil {
				return fmt.Errorf("清理已成功路由尝试 Provider ID %s：%w", item.TaskID, err)
			}
		} else if err := attemptQuery.Updates(attemptUpdates).Error; err != nil {
			return fmt.Errorf("清理路由尝试 Provider ID %s：%w", item.TaskID, err)
		}
	}
	return nil
}

func migrationOtherProviderRequestIDForTask(tx *gorm.DB, item model.ApiCallLog, fabricatedID string) string {
	var candidate model.ApiCallLog
	query := tx.Session(&gorm.Session{Logger: tx.Logger.LogMode(logger.Silent)}).Where("task_id = ? AND id <> ? AND request_kind IN ? AND provider_request_id <> ? AND provider_request_id <> ?", item.TaskID, item.ID, []string{"poll", "download"}, "", fabricatedID)
	if item.ChannelID != "" {
		query = query.Where("channel_id = ?", item.ChannelID)
	}
	if item.Model != "" {
		query = query.Where("model = ?", item.Model)
	}
	if err := query.Order("created_at DESC").First(&candidate).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(candidate.ProviderRequestID)
}

func migrationOtherProviderRequestIDForBilling(tx *gorm.DB, item model.ApiCallLog, fabricatedID string) string {
	var candidate model.ApiCallLog
	query := tx.Session(&gorm.Session{Logger: tx.Logger.LogMode(logger.Silent)}).Where("billing_order_id = ? AND id <> ? AND request_kind IN ? AND provider_request_id <> ? AND provider_request_id <> ?", item.BillingOrderID, item.ID, []string{"poll", "download"}, "", fabricatedID)
	if item.ChannelID != "" {
		query = query.Where("channel_id = ?", item.ChannelID)
	}
	if item.Model != "" {
		query = query.Where("model = ?", item.Model)
	}
	if err := query.Order("created_at DESC").First(&candidate).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(candidate.ProviderRequestID)
}

func migrateChannelCreditCost(tx *gorm.DB) error {
	for _, entity := range []any{&model.ChannelModelPriceTier{}, &model.BillingOrder{}} {
		for _, column := range []string{"cost_configured", "cost_unit_price_microcredits", "cost_input_token_price_microcredits", "cost_output_token_price_microcredits", "cost_cached_token_price_microcredits"} {
			if !tx.Migrator().HasColumn(entity, column) {
				if err := tx.Migrator().AddColumn(entity, column); err != nil {
					return err
				}
			}
		}
	}
	for _, column := range []string{"CostBillingMode", "CostQuantity", "CostVideoFormulaTokens"} {
		if !tx.Migrator().HasColumn(&model.BillingOrder{}, column) {
			if err := tx.Migrator().AddColumn(&model.BillingOrder{}, column); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateChannelModelDescription(tx *gorm.DB) error {
	if tx.Migrator().HasColumn(&model.ChannelModel{}, "Description") {
		return nil
	}
	return tx.Migrator().AddColumn(&model.ChannelModel{}, "Description")
}

func migrateVideoTokenFormulaSnapshot(tx *gorm.DB) error {
	for _, field := range []string{"VideoFormulaTokens", "UsageSource"} {
		if !tx.Migrator().HasColumn(&model.BillingOrder{}, field) {
			if err := tx.Migrator().AddColumn(&model.BillingOrder{}, field); err != nil {
				return fmt.Errorf("增加视频 Token 结算字段 %s：%w", field, err)
			}
		}
	}
	return nil
}

func migrateChannelModelLabel(tx *gorm.DB) error {
	if tx.Migrator().HasColumn(&model.ChannelModel{}, "ChannelLabel") {
		return nil
	}
	return tx.Migrator().AddColumn(&model.ChannelModel{}, "ChannelLabel")
}

func migrateSchemaV14(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.CloudAgentExecution{}); err != nil {
		return err
	}
	// Keep cancellation recoverable for executions admitted before this schema.
	// Bound memory while retaining the migration transaction's all-or-nothing semantics.
	after := ""
	for {
		var runs []model.CloudAgentExecution
		if err := tx.Where("id > ? AND status <> ?", after, "completed").Order("id ASC").Limit(100).Find(&runs).Error; err != nil {
			return err
		}
		if len(runs) == 0 {
			return nil
		}
		for _, run := range runs {
			var state struct {
				Request struct {
					CanvasID string `json:"canvasId"`
				} `json:"request"`
				ActiveTaskID string `json:"activeTaskId"`
				MediaTaskID  string `json:"mediaTaskId"`
			}
			// The root task ID is always a safe cancellation anchor. If an old
			// transcript is damaged, retain a durable warning and cancel that
			// root task during recovery instead of blocking the whole deployment.
			updates := map[string]any{"active_task_id": run.ID}
			if err := json.Unmarshal([]byte(run.StateJSON), &state); err != nil {
				updates["failure_message"] = "旧 Agent 运行记录损坏，已保留根任务并进入安全收尾；请核对任务中心"
			} else {
				updates["canvas_id"] = state.Request.CanvasID
				if state.ActiveTaskID != "" {
					updates["active_task_id"] = state.ActiveTaskID
				}
				updates["media_task_id"] = state.MediaTaskID
			}
			if run.Status == "cancelled" || run.Status == "failed" {
				updates["cleanup_pending"] = true
			}
			if err := tx.Model(&model.CloudAgentExecution{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
				return err
			}
			after = run.ID
		}
	}
}

func migrateChannelPresentation(tx *gorm.DB) error {
	for _, column := range []struct {
		model any
		field string
	}{{&model.ModelChannel{}, "PublicAlias"}, {&model.ModelChannel{}, "SortOrder"}, {&model.ChannelModel{}, "SortOrder"}} {
		if !tx.Migrator().HasColumn(column.model, column.field) {
			if err := tx.Migrator().AddColumn(column.model, column.field); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrationsForDatabase(db *gorm.DB) ([]migration, error) {
	plan := append([]migration(nil), schemaMigrations...)

	var applied schemaMigration
	err := db.First(&applied, "version = ?", 6).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("读取数据库迁移 6：%w", err)
	}
	if err == nil && applied.Name == "asset_library_folders" {
		legacy := migration{version: 6, name: "asset_library_folders", checksum: assetLibraryFoldersChecksum, apply: migrateSchemaV7}
		if err := validateMigrationRecord(applied, legacy); err != nil {
			return nil, err
		}
		for index, item := range plan {
			switch item.version {
			case 6:
				plan[index] = legacy
			case 7:
				plan[index] = migration{version: 7, name: "resource_playback_variant", checksum: resourcePlaybackChecksum, apply: migrateSchemaV6}
			}
		}
	}

	var legacyRelay schemaMigration
	err = db.First(&legacyRelay, "version = ?", 16).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("读取数据库迁移 16：%w", err)
	}
	if err == nil && legacyRelay.Name == "resource_upstream_relay" {
		legacy := migration{
			version:  16,
			name:     "resource_upstream_relay",
			checksum: legacyResourceUpstreamRelayChecksum,
			apply:    func(*gorm.DB) error { return nil },
		}
		if err := validateMigrationRecord(legacyRelay, legacy); err != nil {
			return nil, err
		}
		for index, item := range plan {
			if item.version == 16 {
				plan[index] = legacy
				break
			}
		}
	}

	var v28 schemaMigration
	err = db.First(&v28, "version = ?", 28).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("读取数据库迁移 28：%w", err)
	}
	if err == nil && v28.Name == "resource_upstream_relay" {
		localV28 := migration{
			version:  28,
			name:     "resource_upstream_relay",
			checksum: resourceUpstreamRelayChecksum,
			apply:    func(*gorm.DB) error { return nil },
		}
		if err := validateMigrationRecord(v28, localV28); err != nil {
			return nil, err
		}
		for index, item := range plan {
			if item.version == 28 {
				plan[index] = localV28
				break
			}
		}
	}

	var v30 schemaMigration
	err = db.First(&v30, "version = ?", 30).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("读取数据库迁移 30：%w", err)
	}
	if err == nil && v30.Name == "resource_upstream_relay_v28_reconciliation" {
		localV30 := migration{
			version:  30,
			name:     "resource_upstream_relay_v28_reconciliation",
			checksum: resourceUpstreamRelayReconciliationChecksum,
			apply:    func(*gorm.DB) error { return nil },
		}
		if err := validateMigrationRecord(v30, localV30); err != nil {
			return nil, err
		}
		for index, item := range plan {
			switch item.version {
			case 30:
				plan[index] = localV30
			case 31:
				plan[index] = migration{version: 31, name: "builtin_tools", checksum: "sha256:builtin-tools-v30", apply: migrateBuiltinTools}
			}
		}
	}

	// Upstream v1.5.7 used migration 32 for model tags, while the local
	// branch used the same version for tool-schema reconciliation. Accept that
	// historical upstream record and run the idempotent tags migration again
	// under v33 so both physical schemas converge without rewriting history.
	var v32 schemaMigration
	err = db.First(&v32, "version = ?", 32).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("读取数据库迁移 32：%w", err)
	}
	if err == nil && v32.Name == "channel_model_tags" {
		legacyTags := migration{
			version:  32,
			name:     "channel_model_tags",
			checksum: legacyChannelModelTagsChecksum,
			apply:    func(*gorm.DB) error { return nil },
		}
		if err := validateMigrationRecord(v32, legacyTags); err != nil {
			return nil, err
		}
		for index, item := range plan {
			if item.version == 32 {
				plan[index] = legacyTags
				break
			}
		}
	}
	return plan, nil
}

func migrateSchemaV2(tx *gorm.DB) error {
	return tx.Exec("CREATE INDEX IF NOT EXISTS idx_schema_migrations_applied_at ON schema_migrations (applied_at)").Error
}

func migrateSchemaV3(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.ProjectAssetCandidate{}); err != nil {
		return fmt.Errorf("扩展资产候选身份字段：%w", err)
	}
	if err := tx.Exec("UPDATE assets SET category = 'prop' WHERE category IN ('wardrobe', 'weapon', 'accessory')").Error; err != nil {
		return fmt.Errorf("合并资产道具分类：%w", err)
	}
	if err := tx.Exec("UPDATE assets SET category = 'material' WHERE category = 'style' OR (category = 'other' AND kind IN ('image', 'video', 'audio', 'model'))").Error; err != nil {
		return fmt.Errorf("迁移资产素材分类：%w", err)
	}
	if err := tx.Exec("UPDATE project_asset_candidates SET category = 'prop' WHERE category IN ('wardrobe', 'weapon', 'accessory')").Error; err != nil {
		return fmt.Errorf("合并候选道具分类：%w", err)
	}
	if err := tx.Exec("UPDATE project_asset_candidates SET category = 'material' WHERE category = 'style'").Error; err != nil {
		return fmt.Errorf("迁移候选素材分类：%w", err)
	}
	var candidates []model.ProjectAssetCandidate
	if err := tx.Order("created_at asc, id asc").Find(&candidates).Error; err != nil {
		return fmt.Errorf("读取资产候选身份：%w", err)
	}
	seenPending := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		nameKey := model.AssetCandidateNameKey(candidate.Name)
		updates := map[string]any{"name_key": nameKey}
		identity := candidate.ProjectID + ":" + string(candidate.Category) + ":" + nameKey
		if candidate.Status == "pending_confirmation" && nameKey != "" {
			if _, exists := seenPending[identity]; exists {
				updates["status"] = "ignored"
			} else {
				seenPending[identity] = candidate.ID
			}
		}
		if err := tx.Model(&model.ProjectAssetCandidate{}).Where("id = ?", candidate.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("回填资产候选身份 %s：%w", candidate.ID, err)
		}
	}
	return tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_project_asset_candidates_pending_identity ON project_asset_candidates(project_id, category, name_key) WHERE status = 'pending_confirmation' AND name_key <> ''").Error
}

func migrateSchemaV4(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.Resource{}) {
		return fmt.Errorf("资源表不存在")
	}
	if !tx.Migrator().HasColumn(&model.Resource{}, "upload_key") {
		if err := tx.Migrator().AddColumn(&model.Resource{}, "UploadKey"); err != nil {
			return fmt.Errorf("增加资源上传幂等列：%w", err)
		}
	}
	if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_resources_user_upload_key ON resources (user_id, upload_key)").Error; err != nil {
		return fmt.Errorf("创建资源上传幂等索引：%w", err)
	}
	return nil
}
func migrateSchemaV5(tx *gorm.DB) error {
	if err := tx.AutoMigrate(
		&model.CreditLedgerEntry{},
		&model.TopupProduct{},
		&model.PaymentProviderConfig{},
		&model.PaymentOrder{},
		&model.PaymentNotification{},
		&model.PaymentReconciliationRun{},
		&model.PaymentReconciliationItem{},
	); err != nil {
		return fmt.Errorf("创建积分支付与对账结构：%w", err)
	}
	return nil
}

func migrateSchemaV19(tx *gorm.DB) error {
	for _, value := range []any{&model.PaymentProviderConfig{}, &model.PaymentOrder{}} {
		if !tx.Migrator().HasTable(value) {
			continue
		}
		if err := addPaymentPluginVersionColumn(tx, value); err != nil {
			return err
		}
	}
	return nil
}

func addPaymentPluginVersionColumn(tx *gorm.DB, value any) error {
	if tx.Migrator().HasColumn(value, "plugin_version") {
		return nil
	}
	if err := tx.Migrator().AddColumn(value, "PluginVersion"); err != nil {
		return fmt.Errorf("增加支付插件版本列：%w", err)
	}
	return nil
}

func migrateSchemaV6(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.Resource{}) {
		return fmt.Errorf("资源表不存在")
	}
	if !tx.Migrator().HasColumn(&model.Resource{}, "playback_status") {
		if err := tx.Migrator().AddColumn(&model.Resource{}, "PlaybackStatus"); err != nil {
			return fmt.Errorf("增加播放副本状态列：%w", err)
		}
	}
	if !tx.Migrator().HasColumn(&model.Resource{}, "playback_object_key") {
		if err := tx.Migrator().AddColumn(&model.Resource{}, "PlaybackObjectKey"); err != nil {
			return fmt.Errorf("增加播放副本对象键列：%w", err)
		}
	}
	if !tx.Migrator().HasColumn(&model.Resource{}, "playback_error") {
		if err := tx.Migrator().AddColumn(&model.Resource{}, "PlaybackError"); err != nil {
			return fmt.Errorf("增加播放副本错误列：%w", err)
		}
	}
	return nil
}

func migrateSchemaV7(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.Asset{}, &model.AssetFolder{}); err != nil {
		return fmt.Errorf("创建个人素材分类并扩展素材目录字段：%w", err)
	}
	return nil
}

func migrateSchemaV8(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.LogicalModel{}) {
		return nil
	}
	if err := tx.Exec("DROP INDEX IF EXISTS idx_logical_models_code").Error; err != nil {
		return fmt.Errorf("移除前台模型旧 code 唯一索引：%w", err)
	}
	if err := tx.Exec("CREATE UNIQUE INDEX idx_logical_models_code ON logical_models(code) WHERE archived_at IS NULL").Error; err != nil {
		return fmt.Errorf("创建前台模型活动 code 唯一索引：%w", err)
	}
	return nil
}

// migrateSchemaV10 只增加创作运行时表和任务幂等关联；旧任务的空 submission ID 必须继续合法。
func migrateSchemaV10(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.CreationRun{}, &model.CreationSubmission{}, &model.Task{}); err != nil {
		return fmt.Errorf("创建创作运行时结构：%w", err)
	}
	return nil
}

// migrateSchemaV12 为 Agent 的 Token 计费增加最终扣费上限；旧账单保持 0，继续沿用既有按 usage 结算语义。
func migrateSchemaV12(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.BillingOrder{}) {
		return nil
	}
	if tx.Migrator().HasColumn(&model.BillingOrder{}, "ChargeLimitMicrocredits") {
		return nil
	}
	if err := tx.Migrator().AddColumn(&model.BillingOrder{}, "ChargeLimitMicrocredits"); err != nil {
		return fmt.Errorf("增加 Agent Token 扣费上限列：%w", err)
	}
	return nil
}

func MigrateSchema(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", postgresSchemaMigrationLockID).Error; err != nil {
				return fmt.Errorf("获取数据库迁移锁：%w", err)
			}
		}
		if err := tx.AutoMigrate(&schemaMigration{}); err != nil {
			return fmt.Errorf("初始化数据库迁移记录：%w", err)
		}
		plan, err := migrationsForDatabase(tx)
		if err != nil {
			return err
		}
		for _, item := range plan {
			var applied schemaMigration
			err := tx.First(&applied, "version = ?", item.version).Error
			if err == nil {
				if err := validateMigrationRecord(applied, item); err != nil {
					return err
				}
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("读取数据库迁移 %d：%w", item.version, err)
			}
			if err := item.apply(tx); err != nil {
				return fmt.Errorf("执行数据库迁移 %d（%s）：%w", item.version, item.name, err)
			}
			record := schemaMigration{Version: item.version, Name: item.name, Checksum: item.checksum, AppliedAt: time.Now().UTC()}
			if err := tx.Create(&record).Error; err != nil {
				return fmt.Errorf("记录数据库迁移 %d：%w", item.version, err)
			}
		}
		return RequireSchemaVersion(tx)
	})
}

func ReadSchemaStatus(db *gorm.DB) (SchemaStatus, error) {
	status := SchemaStatus{Expected: CurrentSchemaVersion}
	if !db.Migrator().HasTable(&schemaMigration{}) {
		return status, nil
	}
	if err := db.Model(&schemaMigration{}).Select("COALESCE(MAX(version), 0)").Scan(&status.Current).Error; err != nil {
		return status, fmt.Errorf("读取数据库结构版本：%w", err)
	}
	if status.Current != status.Expected {
		return status, nil
	}
	if err := validateMigrationRecords(db); err != nil {
		return status, err
	}
	status.Ready = true
	return status, nil
}

func validateMigrationRecords(db *gorm.DB) error {
	plan, err := migrationsForDatabase(db)
	if err != nil {
		return err
	}
	for _, item := range plan {
		var applied schemaMigration
		if err := db.First(&applied, "version = ?", item.version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("数据库缺少迁移记录 %d（%s）", item.version, item.name)
			}
			return fmt.Errorf("读取数据库迁移 %d：%w", item.version, err)
		}
		if err := validateMigrationRecord(applied, item); err != nil {
			return err
		}
	}
	return nil
}

func validateMigrationRecord(applied schemaMigration, expected migration) error {
	if applied.Name != expected.name {
		return fmt.Errorf("数据库迁移 %d 名称不一致：记录为 %s，程序期望 %s", expected.version, applied.Name, expected.name)
	}
	if applied.Checksum != expected.checksum {
		return fmt.Errorf("数据库迁移 %d 校验和不一致：记录为 %s，程序期望 %s", expected.version, applied.Checksum, expected.checksum)
	}
	return nil
}

func RequireSchemaVersion(db *gorm.DB) error {
	status, err := ReadSchemaStatus(db)
	if err != nil {
		return err
	}
	if status.Current < status.Expected {
		return fmt.Errorf("数据库结构版本过旧：当前 %d，程序要求 %d，请先执行 migrate-schema up", status.Current, status.Expected)
	}
	if status.Current > status.Expected {
		return fmt.Errorf("数据库结构版本 %d 高于程序支持的 %d，拒绝使用旧程序连接新数据库", status.Current, status.Expected)
	}
	return nil
}
