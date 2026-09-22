package database

import (
	"encoding/json"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestMigrateLXMoneH3CapabilityLimits(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-lxmone-h3-capability?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ChannelModel{}); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"video":{"references":{"maxImages":9},"duration":{"selection":"range","min":1,"max":15,"step":1,"default":15}}}`
	for _, item := range []model.ChannelModel{
		{ID: "h3-a", ModelKey: "minimax-h3-a", ProviderModelKey: "minimax-h3-a", Capability: "video", Protocol: "lxmone-h3-workflow", CapabilityConfigJSON: legacy},
		{ID: "h3-e", ModelKey: "minimax-h3-e", ProviderModelKey: "minimax-h3-e", Capability: "video", Protocol: "lxmone-h3-workflow", CapabilityConfigJSON: legacy},
		{ID: "h3-b", ModelKey: "minimax-h3-b", ProviderModelKey: "minimax-h3-b", Capability: "video", Protocol: "lxmone-h3-workflow", CapabilityConfigJSON: legacy},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := migrateLXMoneH3CapabilityLimits(db); err != nil {
		t.Fatal(err)
	}

	var h3a, h3e, h3b model.ChannelModel
	if err := db.First(&h3a, "id = ?", "h3-a").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&h3e, "id = ?", "h3-e").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&h3b, "id = ?", "h3-b").Error; err != nil {
		t.Fatal(err)
	}
	assertH3MigrationJSON(t, h3a.CapabilityConfigJSON, 12, 9)
	assertH3MigrationJSON(t, h3e.CapabilityConfigJSON, 15, 1)
	assertH3MigrationJSON(t, h3b.CapabilityConfigJSON, 15, 9)
	if h3a.CapabilityVersion != 1 || h3e.CapabilityVersion != 1 || h3b.CapabilityVersion != 0 {
		t.Fatalf("unexpected capability versions: a=%d e=%d b=%d", h3a.CapabilityVersion, h3e.CapabilityVersion, h3b.CapabilityVersion)
	}
}

func TestMigrateLXMoneH3CapabilityClampsDefaultToExistingMaximum(t *testing.T) {
	legacy := `{"version":1,"video":{"references":{"maxImages":9},"duration":{"selection":"range","min":1,"max":10,"step":1,"default":15}}}`
	updated, changed := migrateLXMoneH3CapabilityJSON(legacy, "minimax-h3-a")
	if !changed {
		t.Fatal("legacy H3-A capability was not updated")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(updated), &config); err != nil {
		t.Fatal(err)
	}
	video := config["video"].(map[string]any)
	duration := video["duration"].(map[string]any)
	if int(duration["max"].(float64)) != 10 || int(duration["default"].(float64)) != 10 {
		t.Fatalf("migrated duration = %#v, want max/default 10", duration)
	}
}

func assertH3MigrationJSON(t *testing.T, raw string, wantMaxSeconds int, wantMaxImages int) {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	video := config["video"].(map[string]any)
	duration := video["duration"].(map[string]any)
	references := video["references"].(map[string]any)
	if int(duration["max"].(float64)) != wantMaxSeconds || int(references["maxImages"].(float64)) != wantMaxImages {
		t.Fatalf("migrated config = %#v", config)
	}
}

func TestMigrateFabricatedProviderRequestIDs(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-provider-request-id?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ApiCallLog{}, &model.Task{}, &model.BillingOrder{}, &model.RouteAttempt{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Task{ID: "task-fake", Status: model.TaskStatusFailed, ProviderRequestID: "generations", PollStage: "create"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BillingOrder{ID: "order-fake", UserID: "user-fake", IdempotencyKey: "idem-fake", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RouteAttempt{ID: "attempt-fake", TaskID: "task-fake", RouteRun: 0, AttemptNumber: 1, Status: "failed", DispatchState: "accepted", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ApiCallLog{
		ID: "log-fake", TaskID: "task-fake", BillingOrderID: "order-fake", RequestKind: "create",
		Status: model.ApiCallStatusFailed, Path: "/v1/videos/generations", ProviderRequestID: "generations",
		ResponseBody: `{"error":{"message":"upstream unavailable"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Task{ID: "task-success-fake", Status: model.TaskStatusFailed, ProviderRequestID: "generations", PollStage: "create"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BillingOrder{ID: "order-success-fake", UserID: "user-success-fake", IdempotencyKey: "idem-success-fake", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RouteAttempt{ID: "attempt-success-fake", TaskID: "task-success-fake", RouteRun: 0, AttemptNumber: 1, Status: "failed", DispatchState: "accepted", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ApiCallLog{
		ID: "log-success-fake", TaskID: "task-success-fake", BillingOrderID: "order-success-fake", RequestKind: "create",
		Status: model.ApiCallStatusSucceeded, Path: "/v1/videos/generations", ProviderRequestID: "generations",
		ResponseBody: `{"video":{"url":"https://cdn.example/video.mp4"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Task{ID: "task-explicit-id", Status: model.TaskStatusFailed, ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BillingOrder{ID: "order-explicit-id", UserID: "user-explicit-id", IdempotencyKey: "idem-explicit-id", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RouteAttempt{ID: "attempt-explicit-id", TaskID: "task-explicit-id", RouteRun: 0, AttemptNumber: 1, Status: "failed", DispatchState: "accepted", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ApiCallLog{
		ID: "log-explicit-id", TaskID: "task-explicit-id", BillingOrderID: "order-explicit-id", RequestKind: "create",
		Status: model.ApiCallStatusFailed, Path: "/v1/videos/generations", ProviderRequestID: "generations",
		ResponseBody: `{"id":"request-wrapper","data":{"task_id":"provider-task-1"},"error":{"message":"provider accepted then reported a warning"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := migrateFabricatedProviderRequestIDs(db); err != nil {
		t.Fatal(err)
	}
	var task model.Task
	var order model.BillingOrder
	var attempt model.RouteAttempt
	var log model.ApiCallLog
	if err := db.First(&task, "id = ?", "task-fake").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&order, "id = ?", "order-fake").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attempt, "id = ?", "attempt-fake").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&log, "id = ?", "log-fake").Error; err != nil {
		t.Fatal(err)
	}
	if task.ProviderRequestID != "" || order.ProviderRequestID != "" || log.ProviderRequestID != "" || attempt.ProviderRequestID != "" || attempt.DispatchState != "submission_unknown" {
		t.Fatalf("fabricated provider ID was not cleared: task=%q order=%q log=%q attempt=%#v", task.ProviderRequestID, order.ProviderRequestID, log.ProviderRequestID, attempt)
	}
	var successTask model.Task
	var successOrder model.BillingOrder
	var successAttempt model.RouteAttempt
	var successLog model.ApiCallLog
	if err := db.First(&successTask, "id = ?", "task-success-fake").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&successOrder, "id = ?", "order-success-fake").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&successAttempt, "id = ?", "attempt-success-fake").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&successLog, "id = ?", "log-success-fake").Error; err != nil {
		t.Fatal(err)
	}
	if successTask.ProviderRequestID != "" || successOrder.ProviderRequestID != "" || successLog.ProviderRequestID != "" || successAttempt.ProviderRequestID != "" || successAttempt.DispatchState != "submission_unknown" {
		t.Fatalf("successful create's fabricated Provider ID was not cleared: task=%q order=%q log=%q attempt=%#v", successTask.ProviderRequestID, successOrder.ProviderRequestID, successLog.ProviderRequestID, successAttempt)
	}
	var explicitTask model.Task
	var explicitOrder model.BillingOrder
	var explicitAttempt model.RouteAttempt
	var explicitLog model.ApiCallLog
	if err := db.First(&explicitTask, "id = ?", "task-explicit-id").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&explicitOrder, "id = ?", "order-explicit-id").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&explicitAttempt, "id = ?", "attempt-explicit-id").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&explicitLog, "id = ?", "log-explicit-id").Error; err != nil {
		t.Fatal(err)
	}
	if explicitTask.ProviderRequestID != "provider-task-1" || explicitOrder.ProviderRequestID != "provider-task-1" || explicitLog.ProviderRequestID != "provider-task-1" || explicitAttempt.ProviderRequestID != "provider-task-1" || explicitAttempt.DispatchState != "accepted" {
		t.Fatalf("explicit provider ID was not preserved: task=%q order=%q log=%q attempt=%#v", explicitTask.ProviderRequestID, explicitOrder.ProviderRequestID, explicitLog.ProviderRequestID, explicitAttempt)
	}
}

func TestMigrateFabricatedProviderRequestIDPreservesSuccessfulRouteAttempt(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-provider-request-id-success?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ApiCallLog{}, &model.Task{}, &model.BillingOrder{}, &model.RouteAttempt{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Task{ID: "task-sync-success", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BillingOrder{ID: "order-sync-success", UserID: "user-sync-success", IdempotencyKey: "idem-sync-success", ProviderRequestID: "generations"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RouteAttempt{
		ID: "attempt-sync-success", TaskID: "task-sync-success", RouteRun: 0, AttemptNumber: 1,
		Status: "succeeded", DispatchState: "accepted", ProviderRequestID: "generations",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ApiCallLog{
		ID: "log-sync-success", TaskID: "task-sync-success", BillingOrderID: "order-sync-success",
		RequestKind: "create", Status: model.ApiCallStatusSucceeded, Path: "/v1/videos/generations",
		ProviderRequestID: "generations", ResponseBody: `{"video":{"url":"https://cdn.example/video.mp4"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateFabricatedProviderRequestIDs(db); err != nil {
		t.Fatal(err)
	}
	var attempt model.RouteAttempt
	if err := db.First(&attempt, "id = ?", "attempt-sync-success").Error; err != nil {
		t.Fatal(err)
	}
	if attempt.ProviderRequestID != "" || attempt.DispatchState != "accepted" || attempt.Status != "succeeded" {
		t.Fatalf("successful route attempt changed unexpectedly: %#v", attempt)
	}
}
