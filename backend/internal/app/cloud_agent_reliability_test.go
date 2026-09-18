package app

// Regression contracts for recovery, bounded scheduling and safe submission.
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

func reliableAgentRoot(t *testing.T) (*Service, *gorm.DB, *CloudAgentRun) {
	t.Helper()
	s, db, _, _ := creationTestService(t)
	if err := db.Create(&model.CanvasProject{ID: "agent-canvas", UserID: "user", PayloadJSON: `{"nodes":[]}`}).Error; err != nil {
		t.Fatal(err)
	}
	root, err := s.CreateCloudAgentRun("user", agentTestRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	return s, db, root
}

func TestCloudAgentReliabilitySchedulerHeadOfLine500(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	now := time.Now().Add(-time.Hour)
	var tailID, tailUser string
	profile := cloudAgentProfileSnapshot{Revision: agentProfileRevision(nil), Hash: agentProfileHash("")}
	_, policy, err := compileCloudAgentPolicies(agentTestRequest(), nil, "", profile)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		user := fmt.Sprintf("audit-user-%03d", i)
		req := agentTestRequest()
		req.IdempotencyKey = fmt.Sprintf("audit-key-%03d", i)
		id := cloudAgentID(user, req.IdempotencyKey)
		status := model.TaskStatusSucceeded
		if i < 50 {
			status = model.TaskStatusRunning
		}
		ts := now.Add(time.Duration(i) * time.Millisecond)
		task := model.Task{ID: id, UserID: user, ProjectID: req.CanvasID, Operation: cloudAgentOperation, Status: status, ResultJSON: `{"text":"ready"}`, CreatedAt: ts, UpdatedAt: ts}
		if err := db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
		state := cloudAgentRuntime{Request: req, Policy: policy, Profile: profile, ActiveTaskID: id, TaskIDs: []string{id}, Step: 1, Decisions: map[string]string{}, Events: []CloudAgentEvent{}}
		run := model.CloudAgentExecution{ID: id, UserID: user, Status: "running", Revision: 1, CreatedAt: ts, UpdatedAt: ts}
		if err := cloudAgentSave(&run, &state); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		tailID, tailUser = id, user
	}
	started := time.Now()
	for i := 0; i < 20; i++ {
		s.advanceCloudAgents()
	}
	var completed int64
	if err := db.Model(&model.CloudAgentExecution{}).Where("status = ?", "completed").Count(&completed).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("500 runs, first 50 waiting, 450 ready: completed after 20 scheduler passes=%d; elapsed=%s (not a capacity benchmark)", completed, time.Since(started))
	if completed != 450 {
		t.Fatalf("ready runs starved: %d/450 completed", completed)
	}
	tail, err := s.repo.CloudAgent(tailUser, tailID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.advanceCloudAgent(tail); err != nil {
		t.Fatal(err)
	}
	tail, err = s.repo.CloudAgent(tailUser, tailID)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Status != "completed" {
		t.Fatalf("tail should be ready independently, got %s", tail.Status)
	}
}

func TestCloudAgentReliabilityOversizedCheckpoint(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	run, err := s.repo.CloudAgent("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := cloudAgentDecode(run)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		state.event(root.ID, "tool_completed", map[string]any{"text": strings.Repeat("x", 120000)})
	}
	if err = s.repo.MutateCloudAgent("user", root.ID, run.Revision, func(current *model.CloudAgentExecution, _ *repository.Repository) error {
		return cloudAgentSave(current, &state)
	}); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]string{"text": strings.Repeat("a", 31900)})
	if err = db.Model(&model.Task{}).Where("id = ?", root.ID).Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": string(result)}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		run, err = s.repo.CloudAgent("user", root.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.advanceCloudAgent(run); err != nil {
			t.Fatal(err)
		}
	}
	run, err = s.repo.CloudAgent("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("3 deterministic oversized checkpoint failures: run=%s revision=%d bytes=%d", run.Status, run.Revision, len(run.StateJSON))
	if run.Status != "failed" || run.CleanupPending || run.FailureMessage == "" {
		t.Fatalf("failed run did not settle: %+v", run)
	}
}

func TestCloudAgentReliabilityCancelReplayInterrupted(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	// Simulate process/request interruption after cancelled checkpoint commits, before child cancellation.
	if err := db.Model(&model.CloudAgentExecution{}).Where("id = ?", root.ID).Update("status", "cancelled").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.CancelCloudAgent(context.Background(), "user", root.ID); err != nil {
		t.Fatal(err)
	}
	task, err := s.repo.TaskForUser("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel replay returned success; active child status remains=%s", task.Status)
	if task.Status != model.TaskStatusCancelled {
		t.Fatalf("child not cancelled: %s", task.Status)
	}
}

func TestCloudAgentReliabilityFailedContinuation(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	if err := db.Model(&model.Task{}).Where("id = ?", root.ID).Updates(map[string]any{"status": model.TaskStatusFailed, "error": "mock failure"}).Error; err != nil {
		t.Fatal(err)
	}
	req := agentTestRequest()
	req.Prompt = "继续刚才的任务"
	req.IdempotencyKey = "audit-continuation-key"
	child, err := s.CreateCloudAgentRun("user", req, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.repo.TaskForUser("user", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		TextHistory []providerTextMessage `json:"textHistory"`
	}
	if err = json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	t.Logf("failed first turn -> continue: inherited history messages=%d", len(input.TextHistory))
	if len(input.TextHistory) != 2 || input.TextHistory[0].Content != agentTestRequest().Prompt || !strings.Contains(input.TextHistory[1].Content, "failed") {
		t.Fatalf("continuation lost facts: %+v", input.TextHistory)
	}
}

func TestCloudAgentReliabilityIdleSnapshotReadCost(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	snapshot, err := s.CloudAgentRun("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err = db.Callback().Query().After("gorm:query").Register("reliability:count_queries", func(tx *gorm.DB) {
		queries++
		if strings.Contains(tx.Statement.SQL.String(), "SELECT *") {
			t.Error("idle stream loaded full row")
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove("reliability:count_queries")
	unchanged, err := s.CloudAgentRunIfChanged("user", root.ID, snapshot.Revision)
	if err != nil || unchanged != nil || queries != 1 {
		t.Fatalf("idle read should be one small query: queries=%d run=%v err=%v", queries, unchanged, err)
	}
	if _, err = s.CloudAgentRunIfChanged("another-user", root.ID, snapshot.Revision); err == nil {
		t.Fatal("cross-user stream allowed")
	}
}

func TestCloudAgentReliabilityDirectChannelDispatchGuard(t *testing.T) {
	s, _, root := reliableAgentRoot(t)
	task, err := s.repo.TaskForUser("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Attempts = 1
	attempt, err := s.beginTaskRouteAttempt(task)
	if err != nil || attempt == nil {
		t.Fatalf("missing attempt %v", err)
	}
	stale := *attempt
	if err = s.markRouteAttemptDispatching(attempt); err != nil {
		t.Fatal(err)
	}
	if err = s.markRouteAttemptDispatching(&stale); !isRouteDispatchUncertain(err) {
		t.Fatalf("stale dispatch allowed: %v", err)
	}
	task.Attempts = 2
	if _, err = s.beginTaskRouteAttempt(task); !isRouteDispatchUncertain(err) {
		t.Fatalf("ambiguous retry allowed: %v", err)
	}
	task.ProviderRequestID = "provider-original"
	recovered, err := s.beginTaskRouteAttempt(task)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != attempt.ID || recovered.DispatchState != "accepted" {
		t.Fatalf("did not recover original attempt: %+v", recovered)
	}
	ctx1 := withProviderSubmissionKey(context.Background(), attempt)
	ctx2 := withProviderSubmissionKey(context.Background(), recovered)
	if ctx1.Value(providerSubmissionKeyContext{}) != ctx2.Value(providerSubmissionKeyContext{}) {
		t.Fatal("recovery changed upstream key")
	}
}

func TestCloudAgentReliabilityPendingCancellationRecoveredByScheduler(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	if err := db.Model(&model.CloudAgentExecution{}).Where("id = ?", root.ID).Updates(map[string]any{"status": "cancelled", "cleanup_pending": true}).Error; err != nil {
		t.Fatal(err)
	}
	s.advanceCloudAgents()
	task, err := s.repo.TaskForUser("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.repo.CloudAgent("user", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStatusCancelled || run.CleanupPending {
		t.Fatalf("cancellation not recovered: task=%s pending=%v", task.Status, run.CleanupPending)
	}
	if err = s.CancelCloudAgent(context.Background(), "user", root.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCloudAgentReliabilityCorruptedCancellationUsesControlTask(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	child := model.Task{ID: "cleanup-child", UserID: "user", Status: model.TaskStatusQueued, Operation: "cloud_agent_step"}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CloudAgentExecution{}).Where("id = ?", root.ID).Updates(map[string]any{"state_json": "broken", "active_task_id": child.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.CancelCloudAgent(context.Background(), "user", root.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.repo.TaskForUser("user", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskStatusCancelled {
		t.Fatal("corrupted state orphaned active child")
	}
}

// 画布助手在失败轮之后重发时，客户端会把新轮次挂到失败轮的父轮，而不是失败轮
// 本身。这里锁定该合同的关键收益：新轮次的历史里只有干净的成功上下文，
// 不含"本轮已停止"这类失败事实，模型不会把失败当成已确认的前提。
func TestCloudAgentReliabilityRetryAfterFailureSkipsFailedTurnHistory(t *testing.T) {
	s, db, root := reliableAgentRoot(t)
	// 第一轮成功，成为干净父轮。
	if err := db.Model(&model.Task{}).Where("id = ?", root.ID).Updates(map[string]any{
		"status": model.TaskStatusSucceeded, "result_json": `{"text":"可信回复"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CloudAgentExecution{}).Where("id = ?", root.ID).Updates(map[string]any{"status": "completed", "active_task_id": ""}).Error; err != nil {
		t.Fatal(err)
	}

	// 第二轮失败：它带着自己的提示词和失败摘要。
	failed := agentTestRequest()
	failed.Prompt = "适合视频模型的完整生成提示词"
	failed.IdempotencyKey = "audit-retry-failed-key"
	failedRun, err := s.CreateCloudAgentRun("user", failed, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", failedRun.ID).Updates(map[string]any{
		"status": model.TaskStatusFailed, "error": "上游模型服务暂时过载或不可用，请稍后重试",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.advanceCloudAgentByID("user", failedRun.ID); err != nil {
		t.Fatal(err)
	}

	// 失败轮自己作为父轮时，历史里确实带着失败事实——这正是重发必须避开它的原因。
	viaFailed := agentTestRequest()
	viaFailed.Prompt = "重发"
	viaFailed.IdempotencyKey = "audit-retry-via-failed-key"
	viaFailedRun, err := s.CreateCloudAgentRun("user", viaFailed, failedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	var failedInput struct {
		TextHistory []providerTextMessage `json:"textHistory"`
	}
	task, err := s.repo.TaskForUser("user", viaFailedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(task.InputJSON), &failedInput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failedHistoryText(failedInput.TextHistory), "run_failed") {
		t.Fatalf("failed parent did not carry failure facts: %s", failedHistoryText(failedInput.TextHistory))
	}

	// 重发路径：挂回干净父轮，历史里不含失败事实。
	retry := agentTestRequest()
	retry.Prompt = "适合视频模型的完整生成提示词"
	retry.IdempotencyKey = "audit-retry-clean-key"
	retryRun, err := s.CreateCloudAgentRun("user", retry, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.repo.TaskForUser("user", retryRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	var retryInput struct {
		TextHistory []providerTextMessage `json:"textHistory"`
	}
	if err = json.Unmarshal([]byte(task.InputJSON), &retryInput); err != nil {
		t.Fatal(err)
	}
	history := failedHistoryText(retryInput.TextHistory)
	if strings.Contains(history, "本轮已停止") || strings.Contains(history, "run_failed") {
		t.Fatalf("retry inherited failure context from the failed turn: %s", history)
	}
	if !strings.Contains(history, "可信回复") {
		t.Fatalf("retry lost the clean prior turn: %s", history)
	}
}

func failedHistoryText(history []providerTextMessage) string {
	encoded, err := json.Marshal(history)
	if err != nil {
		return ""
	}
	return string(encoded)
}
