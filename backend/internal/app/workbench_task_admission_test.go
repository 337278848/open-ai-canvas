package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
	"yingce/backend/internal/platform"
	"yingce/backend/internal/workbench"
)

// workbenchVideoPathRecorder 记录供应商请求路径，证明已受理的视频恢复只轮询原任务、
// 绝不重新提交；访问都发生在测试协程，无需加锁。
type workbenchVideoPathRecorder struct {
	paths       []string
	pollCount   atomic.Int32
	createCount atomic.Int32
}

func (r *workbenchVideoPathRecorder) record(method, path string) {
	r.paths = append(r.paths, method+" "+path)
	if method == http.MethodPost {
		r.createCount.Add(1)
	}
	if method == http.MethodGet && strings.HasPrefix(path, "/v1/video/generations/") {
		r.pollCount.Add(1)
	}
}

func workbenchVideoServer(t *testing.T, id string, recorder *workbenchVideoPathRecorder) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Method, r.URL.Path)
		path := r.Method + " " + r.URL.Path
		switch path {
		case "GET /v1/video/generations/" + id:
			w.Header().Set("Content-Type", "application/json")
			resultURL := "http://" + r.Host + "/video.mp4"
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "success", "data": map[string]any{"task_id": id, "status": "SUCCESS", "result_url": resultURL}})
		case "GET /video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("workbench video"))
		default:
			if strings.HasPrefix(r.URL.Path, "/v1/video/generations/") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("unknown provider task"))
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func workbenchVideoFixture(t *testing.T) (*Service, *gorm.DB, WorkbenchTaskInput, model.ModelChannel, model.ChannelModel) {
	t.Helper()
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	profile := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceNewAPIChannel2), "workbench-video")
	svc, db, channel, channelModel := createSystemChannelSelectionFixture(t, "video", model.ChannelInterfaceNewAPIChannel2, profile, []model.ChannelModelPriceTier{
		newSelectionPriceTier("workbench-video-tier", `{}`, "workbench-video", "fixed_request"),
	})
	if err := db.AutoMigrate(database.Models()...); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	svc = New(svc.repo, t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	if err := db.Create(&model.SystemSetting{Key: featureAvailabilitySettingKey, ValueJSON: `{"videoWorkbenchEnabled":true}`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CreditAccount{UserID: "user", AvailableMicrocredits: 100000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.WorkbenchTool{ID: "video-tool", Kind: WorkbenchKindVideo, Name: "视频工具", Enabled: true, CapabilityJSON: `{"inputContract":{"promptRequired":false,"slots":[]},"referenceKinds":[]}`, GuideJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	req := WorkbenchTaskInput{Kind: WorkbenchKindVideo, ToolID: "video-tool", Prompt: "  ", ChannelID: channel.ID, Model: channelModel.ModelKey, Parameters: map[string]any{"seconds": 5}}
	return svc, db, req, channel, channelModel
}

func TestWorkbenchVideoResumesOriginalProviderTaskWithoutResubmitting(t *testing.T) {
	svc, db, req, channel, _ := workbenchVideoFixture(t)
	task, err := svc.CreateWorkbenchTask("user", req, "trace", "request")
	if err != nil {
		t.Fatal(err)
	}
	const providerID = "existing-provider-task"
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{"provider_request_id": providerID, "status": model.TaskStatusQueued}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WorkbenchTool{}).Where("id = ?", "video-tool").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	recorder := &workbenchVideoPathRecorder{}
	server := workbenchVideoServer(t, providerID, recorder)
	if err := db.Model(&model.ModelChannel{}).Where("id = ?", channel.ID).Updates(map[string]any{"base_url": server.URL, "api_key": "test-only"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessNextTask(); err != nil {
		t.Fatal(err)
	}
	var stored model.Task
	if err := db.First(&stored, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.TaskStatusSucceeded {
		t.Fatalf("recovered video task status = %s, want succeeded", stored.Status)
	}
	if recorder.createCount.Load() != 0 {
		t.Fatalf("recovery re-submitted the provider task: %v", recorder.paths)
	}
	if recorder.pollCount.Load() < 1 {
		t.Fatalf("recovery did not poll the original provider task: %v", recorder.paths)
	}
	var order model.BillingOrder
	if err := db.First(&order, "id = ?", stored.BillingOrderID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != model.BillingStatusSettled {
		t.Fatalf("recovered video billing = %s, want settled", order.Status)
	}
}

func TestWorkbenchVideoRejectsInconsistentResumeCredentials(t *testing.T) {
	for _, name := range []string{"mismatched database id", "missing database id"} {
		t.Run(name, func(t *testing.T) {
			svc, db, req, channel, _ := workbenchVideoFixture(t)
			task, err := svc.CreateWorkbenchTask("user", req, "trace", "request")
			if err != nil {
				t.Fatal(err)
			}
			const providerID = "existing-provider-task"
			updates := map[string]any{"status": model.TaskStatusQueued}
			switch name {
			case "mismatched database id":
				updates["provider_request_id"] = "forged-provider-task"
			case "missing database id":
				updates["provider_request_id"] = ""
			}
			if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.WorkbenchTool{}).Where("id = ?", "video-tool").Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
			recorder := &workbenchVideoPathRecorder{}
			server := workbenchVideoServer(t, providerID, recorder)
			if err := db.Model(&model.ModelChannel{}).Where("id = ?", channel.ID).Updates(map[string]any{"base_url": server.URL, "api_key": "test-only"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.ProcessNextTask(); err == nil {
				t.Fatal("invalid resume credentials executed without error")
			}
			if recorder.createCount.Load() != 0 {
				t.Fatalf("invalid resume credentials created a provider task: %v", recorder.paths)
			}
			var stored model.Task
			if err := db.First(&stored, "id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status == model.TaskStatusSucceeded {
				t.Fatal("invalid resume credentials reached provider success")
			}
		})
	}
}

func workbenchTaskAdmissionFixture(t *testing.T) (*Service, *gorm.DB, WorkbenchTaskInput) {
	t.Helper()
	profile := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceOpenAIImage), "gpt-image-1")
	svc, db, channel, channelModel := createSystemChannelSelectionFixture(t, "image", model.ChannelInterfaceOpenAIImage, profile, []model.ChannelModelPriceTier{
		newSelectionPriceTier("workbench-tier", `{}`, "gpt-image-1", "fixed_request"),
	})
	if err := db.AutoMigrate(database.Models()...); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	svc.dataDir = t.TempDir()
	for _, item := range []any{
		&model.SystemSetting{Key: featureAvailabilitySettingKey, ValueJSON: `{"imageWorkbenchEnabled":true}`},
		&model.CreditAccount{UserID: "user", AvailableMicrocredits: 10000},
		&model.WorkbenchTool{ID: "optional-tool", Kind: WorkbenchKindImage, Name: "参考图工具", Enabled: true, CapabilityJSON: `{"referenceKinds":["image"],"inputContract":{"promptRequired":false,"slots":[{"key":"subject","name":"主体","mediaTypes":["image"],"maxCount":1,"minCount":1,"required":true}]}}`, GuideJSON: "{}"},
		&model.Resource{ID: "image", UserID: "user", Kind: "image", Status: model.ResourceStatusReady},
	} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	return svc, db, WorkbenchTaskInput{Kind: WorkbenchKindImage, ToolID: "optional-tool", Prompt: "  ", ChannelID: channel.ID, Model: channelModel.ModelKey, References: []workbench.TaskReference{{ResourceID: "image", Kind: "image", SlotKey: "subject"}}}
}

func TestWorkbenchOptionalPromptReachesTaskAndBillingAdmission(t *testing.T) {
	svc, db, req := workbenchTaskAdmissionFixture(t)
	task, err := svc.CreateWorkbenchTask("user", req, "trace", "request")
	if err != nil {
		t.Fatal(err)
	}
	if task.Prompt != "" || task.WorkbenchKind != WorkbenchKindImage || task.BillingOrderID == "" {
		t.Fatalf("task did not preserve optional prompt and normal billing: %#v", task)
	}
	persisted, err := svc.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	inputJSON, err := svc.decryptTaskInputJSON(persisted.InputJSON)
	if err != nil {
		t.Fatal(err)
	}
	var input canvasGenerationInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if input.Prompt != "" || len(input.ReferenceImages) != 1 || input.ReferenceImages[0].StorageKey != "resource:image" {
		t.Fatalf("authorized input was changed: %#v", input)
	}
	var orders int64
	if err := db.Model(&model.BillingOrder{}).Count(&orders).Error; err != nil || orders != 1 {
		t.Fatalf("orders=%d err=%v", orders, err)
	}
	var account model.CreditAccount
	if err := db.First(&account, "user_id = ?", "user").Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicrocredits != 9990 || account.ReservedMicrocredits != 10 {
		t.Fatalf("normal credit reservation missing: %#v", account)
	}
}

func TestWorkbenchOptionalPromptRejectsInvalidInputBeforeCharging(t *testing.T) {
	for _, name := range []string{"missing reference", "foreign reference", "required prompt", "legacy prompt", "no tool", "model output limit"} {
		t.Run(name, func(t *testing.T) {
			svc, db, req := workbenchTaskAdmissionFixture(t)
			switch name {
			case "missing reference":
				req.References = nil
			case "foreign reference":
				if err := db.Model(&model.Resource{}).Where("id = ?", "image").Update("user_id", "other").Error; err != nil {
					t.Fatal(err)
				}
			case "required prompt", "legacy prompt":
				capability := `{"inputContract":{"promptRequired":true}}`
				if name == "legacy prompt" {
					capability = `{}`
				}
				if err := db.Model(&model.WorkbenchTool{}).Where("id = ?", req.ToolID).Update("capability_json", capability).Error; err != nil {
					t.Fatal(err)
				}
			case "no tool":
				req.ToolID = ""
			case "model output limit":
				req.Parameters = map[string]any{"count": 100}
			}
			if _, err := svc.CreateWorkbenchTask("user", req, "", ""); err == nil {
				t.Fatal("invalid request created a task")
			}
			for _, table := range []any{&model.Task{}, &model.BillingOrder{}} {
				var count int64
				if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("rejected request persisted %T count=%d err=%v", table, count, err)
				}
			}
			var account model.CreditAccount
			if err := db.First(&account, "user_id = ?", "user").Error; err != nil {
				t.Fatal(err)
			}
			if account.AvailableMicrocredits != 10000 || account.ReservedMicrocredits != 0 {
				t.Fatalf("rejected request reserved credit: %#v", account)
			}
		})
	}
}

func TestCreateTaskCannotDeclareOptionalWorkbenchPrompt(t *testing.T) {
	var req CreateTaskRequest
	if err := json.Unmarshal([]byte(`{"type":"canvas_image","prompt":"","promptOptional":true,"workbenchPromptOptional":true,"workbenchKind":"image","input":{"metadata":{"source":"workbench","workbenchToolId":"optional-tool"}}}`), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Service{}).CreateTask("user", req); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("public task API bypassed prompt admission: %v", err)
	}
}

func workbenchOptionalPromptExecutionFixture(t *testing.T) (*Service, *gorm.DB, *model.Task, *atomic.Int32) {
	t.Helper()
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	svc, db, req := workbenchTaskAdmissionFixture(t)
	svc.coordinator = platform.NewCoordinatorWithRedis(nil, "workbench-optional-prompt-test")
	picture := mediaTestPNG(t)
	if err := os.MkdirAll(filepath.Join(svc.dataDir, "resources"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.dataDir, "resources", "image.png"), picture, 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Resource{}).Where("id = ?", "image").Updates(map[string]any{"provider": "local", "object_key": "image.png", "mime_type": "image/png", "size": len(picture)}).Error; err != nil {
		t.Fatal(err)
	}
	requests := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/models/gemini-image-test:generateContent") {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
		}
		var body geminiImageRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider input: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Contents) != 1 || len(body.Contents[0].Parts) != 1 || body.Contents[0].Parts[0].Text != "" || body.Contents[0].Parts[0].InlineData == nil || body.Contents[0].Parts[0].InlineData.Data != base64.StdEncoding.EncodeToString(picture) {
			t.Errorf("optional prompt did not deliver the admitted reference alone: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(picture)}}}}}}})
	}))
	t.Cleanup(server.Close)
	if err := db.Model(&model.ModelChannel{}).Where("id = ?", req.ChannelID).Updates(map[string]any{"base_url": server.URL, "api_key": "test-only"}).Error; err != nil {
		t.Fatal(err)
	}
	profile := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceGeminiImage), "gemini-image-test")
	if err := db.Model(&model.ChannelModel{}).Where("channel_id = ?", req.ChannelID).Updates(map[string]any{"protocol": model.ChannelInterfaceGeminiImage, "capability_config_json": mustEncodeModelCapabilityConfig(t, profile)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ChannelModelPriceTier{}).Where("id = ?", "workbench-tier").Update("provider_model_key", "gemini-image-test").Error; err != nil {
		t.Fatal(err)
	}
	task, err := svc.CreateWorkbenchTask("user", req, "trace", "request")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := svc.repo.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, db, persisted, requests
}

func TestWorkbenchOptionalPromptExecutesWithValidatedReference(t *testing.T) {
	svc, _, task, requests := workbenchOptionalPromptExecutionFixture(t)
	result, _, err := svc.processTask(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if result["mode"] != "image" || requests.Load() != 1 {
		t.Fatalf("result=%#v provider requests=%d", result, requests.Load())
	}
}

func TestWorkbenchOptionalPromptExecutionRechecksAuthority(t *testing.T) {
	for _, name := range []string{"disabled tool", "required prompt", "foreign reference", "missing snapshot", "ordinary task"} {
		t.Run(name, func(t *testing.T) {
			svc, db, task, requests := workbenchOptionalPromptExecutionFixture(t)
			switch name {
			case "disabled tool":
				if err := db.Model(&model.WorkbenchTool{}).Where("id = ?", "optional-tool").Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			case "required prompt":
				if err := db.Model(&model.WorkbenchTool{}).Where("id = ?", "optional-tool").Update("capability_json", `{"inputContract":{"promptRequired":true}}`).Error; err != nil {
					t.Fatal(err)
				}
			case "foreign reference":
				if err := db.Model(&model.Resource{}).Where("id = ?", "image").Update("user_id", "other").Error; err != nil {
					t.Fatal(err)
				}
			case "missing snapshot":
				raw, err := svc.decryptTaskInputJSON(task.InputJSON)
				if err != nil {
					t.Fatal(err)
				}
				var input canvasGenerationInput
				if err := json.Unmarshal([]byte(raw), &input); err != nil {
					t.Fatal(err)
				}
				delete(input.Metadata, "workbench")
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				task.InputJSON = string(encoded)
			case "ordinary task":
				if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("workbench_kind", "").Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := svc.processTask(context.Background(), *task); err == nil {
				t.Fatal("unauthorized optional prompt was executed")
			}
			if requests.Load() != 0 {
				t.Fatalf("invalid task made %d provider requests", requests.Load())
			}
		})
	}
}
