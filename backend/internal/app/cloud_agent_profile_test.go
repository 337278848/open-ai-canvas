package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"infinite-canvas/backend/internal/model"
)

func cloudAgentProfileFixture(t *testing.T) (*Service, string, string) {
	s, _, projectID, canvasID := cloudAgentProfileFixtureDB(t)
	return s, projectID, canvasID
}

func cloudAgentProfileFixtureDB(t *testing.T) (*Service, *gorm.DB, string, string) {
	t.Helper()
	s, db, _, _ := creationTestService(t)
	now := time.Now()
	for _, item := range []any{
		&model.Project{ID: "project-1", UserID: "user", Name: "Agent 项目", Status: model.ProjectStatusActive, CreatedAt: now, UpdatedAt: now},
		&model.Project{ID: "project-2", UserID: "other", Name: "其他用户项目", Status: model.ProjectStatusActive, CreatedAt: now, UpdatedAt: now},
		&model.CanvasProject{ID: "agent-canvas", UserID: "user", ProjectID: "project-1", Title: "Agent 画布", PayloadJSON: `{"nodes":[],"connections":[]}`, CreatedAt: now, UpdatedAt: now},
		&model.CanvasProject{ID: "foreign-canvas", UserID: "other", ProjectID: "project-2", Title: "其他用户画布", PayloadJSON: `{"nodes":[],"connections":[]}`, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	return s, db, "project-1", "agent-canvas"
}

func saveAgentProfileForTest(t *testing.T, s *Service, userID string, req AgentProfileRequest) AgentProfileView {
	t.Helper()
	view, err := s.UpdateCloudAgentProfile(userID, req)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestCloudAgentProfileMergesUserProjectCanvasInOrder(t *testing.T) {
	s, projectID, canvasID := cloudAgentProfileFixture(t)
	saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "用户偏好", Revision: 0})
	saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeProject, ProjectID: projectID, Content: "项目偏好", Revision: 0})
	view := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeCanvas, CanvasID: canvasID, Content: "画布偏好", Revision: 0})

	if len(view.Layers) != 3 {
		t.Fatalf("expected three profile layers, got %#v", view.Layers)
	}
	for index, scope := range []string{model.AgentProfileScopeUser, model.AgentProfileScopeProject, model.AgentProfileScopeCanvas} {
		if view.Layers[index].Scope != scope {
			t.Fatalf("profile precedence changed: %#v", view.Layers)
		}
	}
	if view.Layers[1].ProjectID != projectID || view.Layers[2].ProjectID != projectID || view.Layers[2].CanvasID != canvasID {
		t.Fatalf("profile scope identity was not normalized: %#v", view.Layers)
	}
	text := agentProfileText(view.Layers)
	if strings.Index(text, "用户偏好") > strings.Index(text, "项目偏好") || strings.Index(text, "项目偏好") > strings.Index(text, "画布偏好") {
		t.Fatalf("profile text precedence changed: %q", text)
	}
	if view.Revision == "" || view.Hash != agentProfileHash(text) {
		t.Fatal("effective profile identifiers do not match the merged document")
	}
}

func TestCloudAgentProfileOwnershipAndScopeValidation(t *testing.T) {
	s, projectID, canvasID := cloudAgentProfileFixture(t)
	tests := []struct {
		name   string
		userID string
		req    AgentProfileRequest
	}{
		{name: "foreign project update", userID: "user", req: AgentProfileRequest{Scope: model.AgentProfileScopeProject, ProjectID: "project-2", Content: "x"}},
		{name: "foreign canvas update", userID: "user", req: AgentProfileRequest{Scope: model.AgentProfileScopeCanvas, CanvasID: "foreign-canvas", Content: "x"}},
		{name: "canvas project mismatch", userID: "user", req: AgentProfileRequest{Scope: model.AgentProfileScopeCanvas, ProjectID: "project-mismatch", CanvasID: canvasID, Content: "x"}},
		{name: "user scope carries project", userID: "user", req: AgentProfileRequest{Scope: model.AgentProfileScopeUser, ProjectID: projectID, Content: "x"}},
		{name: "project scope missing id", userID: "user", req: AgentProfileRequest{Scope: model.AgentProfileScopeProject, Content: "x"}},
		{name: "canvas scope missing id", userID: "user", req: AgentProfileRequest{Scope: model.AgentProfileScopeCanvas, Content: "x"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.UpdateCloudAgentProfile(test.userID, test.req); err == nil {
				t.Fatal("invalid or foreign profile update was accepted")
			}
		})
	}
	if _, err := s.CloudAgentProfileForScope("user", "project-2", ""); err == nil {
		t.Fatal("foreign project profile was readable")
	}
	if _, err := s.CloudAgentProfileForScope("user", "", "foreign-canvas"); err == nil {
		t.Fatal("foreign canvas profile was readable")
	}
	if _, err := s.CloudAgentProfileForScope("user", "project-mismatch", canvasID); err == nil {
		t.Fatal("canvas was readable through a mismatched project")
	}
}

func TestCloudAgentProfileRevisionCASAndNormalization(t *testing.T) {
	s, _, _ := cloudAgentProfileFixture(t)
	if _, err := s.UpdateCloudAgentProfile("user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "first", Revision: 1}); err == nil {
		t.Fatal("first write accepted a non-zero revision")
	}
	first := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "  first  \n", Revision: 0})
	layer := first.Layers[0]
	if layer.Revision != 1 || layer.Content != "first" || layer.Hash != agentProfileHash("first") {
		t.Fatalf("first revision was not normalized: %#v", layer)
	}
	if _, err := s.UpdateCloudAgentProfile("user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "stale", Revision: 0}); err == nil {
		t.Fatal("stale profile revision was accepted")
	}
	second := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "second", Revision: layer.Revision})
	if second.Layers[0].Revision != 2 || second.Layers[0].Hash != agentProfileHash("second") {
		t.Fatalf("profile revision did not advance: %#v", second.Layers[0])
	}
	blank := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: " \n\t ", Revision: second.Layers[0].Revision})
	if blank.Layers[0].Content != "" || agentProfileText(blank.Layers) != "" {
		t.Fatalf("blank profile still injects behavior text: %#v", blank)
	}
}

func TestCloudAgentProfileRejectsUnsafeContent(t *testing.T) {
	s, _, _ := cloudAgentProfileFixture(t)
	if _, err := s.UpdateCloudAgentProfile("", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "unowned"}); err == nil {
		t.Fatal("profile update accepted an unauthenticated owner")
	}
	tests := []struct {
		name    string
		content string
	}{
		{name: "too long", content: strings.Repeat("界", cloudAgentProfileMaxRunes+1)},
		{name: "control character", content: "safe\x00unsafe"},
		{name: "invalid utf8", content: string([]byte{0xff, 0xfe})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.UpdateCloudAgentProfile("user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: test.content}); err == nil {
				t.Fatal("unsafe profile content was accepted")
			}
		})
	}
}

func TestCloudAgentProfileEmptyViewAndRunSnapshotAreImmutable(t *testing.T) {
	s, _, canvasID := cloudAgentProfileFixture(t)
	empty, err := s.CloudAgentProfile("user", canvasID)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Layers) != 0 || agentProfileText(empty.Layers) != "" || empty.Hash != agentProfileHash("") {
		t.Fatalf("unexpected empty profile behavior: %#v", empty)
	}

	profile := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "固定偏好", Revision: 0})
	req := agentTestRequest()
	req.IdempotencyKey = "profile-snapshot-run"
	req.ProfileRevision = profile.Revision
	run, err := s.CreateCloudAgentRun("user", req, "")
	if err != nil {
		t.Fatal(err)
	}
	saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "后来修改", Revision: profile.Layers[0].Revision})

	task, err := s.repo.TaskForUser("user", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		CloudAgent cloudAgentState `json:"cloudAgent"`
		Config     struct {
			SystemPrompt string `json:"systemPrompt"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if input.CloudAgent.Policy.ProfileRevision != profile.Revision || input.CloudAgent.Policy.ProfileHash != profile.Hash {
		t.Fatalf("run profile snapshot changed: %#v", input.CloudAgent.Policy)
	}
	if strings.Contains(input.Config.SystemPrompt, "固定偏好") || strings.Contains(input.Config.SystemPrompt, "后来修改") {
		t.Fatal("profile body leaked into the system prompt")
	}
	if len(input.CloudAgent.Profile.Layers) != 1 || input.CloudAgent.Profile.Layers[0].Content != "固定偏好" {
		t.Fatal("run did not preserve the admitted profile body outside the system prompt")
	}
	state := cloudAgentRuntime{Profile: input.CloudAgent.Profile}
	call := cloudAgentCall{}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"user"}`
	result, err := cloudAgentReadTool(nil, "user", &state, call)
	if err != nil || result.(map[string]any)["content"] != "固定偏好" {
		t.Fatalf("fixed profile could not be read on demand: %v, %v", result, err)
	}
	if _, err := cloudAgentReadTool(nil, "user", &state, call); err == nil {
		t.Fatal("profile layer could be read repeatedly")
	}
	stale := agentTestRequest()
	stale.IdempotencyKey = "profile-stale-run"
	stale.ProfileRevision = profile.Revision
	if _, err := s.CreateCloudAgentRun("user", stale, ""); err == nil {
		t.Fatal("run admission accepted a stale profile revision")
	}
}

func TestCloudAgentContinuationReadsLatestProfileWithoutChangingParent(t *testing.T) {
	s, db, _, canvasID := cloudAgentProfileFixtureDB(t)
	firstProfile := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "第一版专属口吻", Revision: 0})
	first := agentTestRequest()
	first.IdempotencyKey = "profile-parent-key"
	first.ProfileRevision = firstProfile.Revision
	parent, err := s.CreateCloudAgentRun("user", first, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", parent.ID).Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": `{"text":"已完成"}`}).Error; err != nil {
		t.Fatal(err)
	}
	secondProfile := saveAgentProfileForTest(t, s, "user", AgentProfileRequest{Scope: model.AgentProfileScopeUser, Content: "第二版专属口吻", Revision: firstProfile.Layers[0].Revision})
	childRequest := agentTestRequest()
	childRequest.CanvasID = canvasID
	childRequest.IdempotencyKey = "profile-child-key"
	childRequest.ProfileRevision = secondProfile.Revision
	child, err := s.CreateCloudAgentRun("user", childRequest, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, wantRevision, wantText, unwantedText string
	}{
		{parent.ID, firstProfile.Revision, "第一版专属口吻", "第二版专属口吻"},
		{child.ID, secondProfile.Revision, "第二版专属口吻", "第一版专属口吻"},
	} {
		task, err := s.repo.TaskForUser("user", item.id)
		if err != nil {
			t.Fatal(err)
		}
		var input struct {
			CloudAgent cloudAgentState `json:"cloudAgent"`
			Config     struct {
				SystemPrompt string `json:"systemPrompt"`
			} `json:"config"`
		}
		if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
			t.Fatal(err)
		}
		if input.CloudAgent.Policy.ProfileRevision != item.wantRevision || strings.Contains(input.Config.SystemPrompt, item.wantText) || strings.Contains(input.Config.SystemPrompt, item.unwantedText) || len(input.CloudAgent.Profile.Layers) != 1 || input.CloudAgent.Profile.Layers[0].Content != item.wantText {
			t.Fatalf("run %s did not preserve its profile snapshot", item.id)
		}
	}
}

func cloudAgentToolNamesFromTaskInput(t *testing.T, raw string) []string {
	t.Helper()
	var input struct {
		AgentRequests struct {
			Canonical struct {
				Tools []map[string]any `json:"tools"`
			} `json:"canonical"`
		} `json:"agentRequests"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(input.AgentRequests.Canonical.Tools))
	for _, tool := range input.AgentRequests.Canonical.Tools {
		fn, _ := tool["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func TestCloudAgentOmitsProfileReadWhenNoLayers(t *testing.T) {
	s, _, canvasID := cloudAgentProfileFixture(t)
	req := agentTestRequest()
	req.CanvasID = canvasID
	req.IdempotencyKey = "profile-empty-tools"
	run, err := s.CreateCloudAgentRun("user", req, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.repo.TaskForUser("user", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cloudAgentToolNamesFromTaskInput(t, task.InputJSON) {
		if name == "agent_profile_read" {
			t.Fatal("empty profile snapshot still exposed agent_profile_read")
		}
	}
	call := cloudAgentCall{}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"user"}`
	_, err = cloudAgentReadTool(nil, "user", &cloudAgentRuntime{}, call)
	if err == nil || !strings.Contains(err.Error(), "本轮没有长期偏好层") {
		t.Fatalf("empty profile read should refuse: %v", err)
	}
}

func TestCloudAgentProfileReadListsAvailableLayers(t *testing.T) {
	state := cloudAgentRuntime{Profile: cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "only-user"}}}}
	call := cloudAgentCall{}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"canvas"}`
	_, err := cloudAgentReadTool(nil, "user", &state, call)
	if err == nil || !strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "请只读取系统清单列出的层") {
		t.Fatalf("missing layer should list readable scopes: %v", err)
	}
}

func TestCloudAgentReadToolCacheReplaysReadResultsAndErrors(t *testing.T) {
	state := &cloudAgentRuntime{Profile: cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "固定偏好"}}}}
	call := cloudAgentCall{ID: "profile-read"}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"user"}`

	first, err := cloudAgentReadToolCached(nil, "user", state, call)
	if err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	second, err := cloudAgentReadToolCached(nil, "user", state, call)
	if err != nil {
		t.Fatalf("cached read failed: %v", err)
	}
	if first.(map[string]any)["content"] == "" || second.(map[string]any)["cacheReplay"] != true || second.(map[string]any)["replayCount"] != 1 {
		t.Fatalf("cached read did not return a replay receipt: first=%#v second=%#v", first, second)
	}
	var cached map[string]any
	if err := json.Unmarshal(state.ToolReadResults[cloudAgentReadCacheKey(call)].Result, &cached); err != nil || cached["content"] != "固定偏好" {
		t.Fatalf("full cached result was not retained: %#v err=%v", state.ToolReadResults, err)
	}

	bad := call
	bad.Function.Arguments = `{"bogus":1}`
	_, firstErr := cloudAgentReadToolCached(nil, "user", state, bad)
	_, secondErr := cloudAgentReadToolCached(nil, "user", state, bad)
	if firstErr == nil || secondErr == nil || firstErr.Error() != secondErr.Error() {
		t.Fatalf("cached read error changed: first=%v second=%v", firstErr, secondErr)
	}
	if !state.ToolReadResults[cloudAgentReadCacheKey(bad)].ArgumentError || len(state.ToolReadResults) != 2 {
		t.Fatalf("unexpected read cache entries: %#v", state.ToolReadResults)
	}
}

func TestCloudAgentReadToolCacheReplaysRepeatedIdenticalReads(t *testing.T) {
	state := &cloudAgentRuntime{Profile: cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "固定偏好"}}}}
	call := cloudAgentCall{ID: "profile-read"}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"user"}`

	if _, err := cloudAgentReadToolCached(nil, "user", state, call); err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	if _, err := cloudAgentReadToolCached(nil, "user", state, call); err != nil {
		t.Fatalf("the first cached replay should still be allowed: %v", err)
	}
	third, err := cloudAgentReadToolCached(nil, "user", state, call)
	var loopErr *cloudAgentReadLoopError
	if !errors.As(err, &loopErr) {
		t.Fatalf("expected repeated-read guard, got %v", err)
	}
	if third != nil || loopErr.Count != cloudAgentMaxCachedReadReplays+1 {
		t.Fatalf("unexpected repeated-read guard result: result=%#v err=%v", third, err)
	}
	if state.ReadToolCalls != 1 {
		t.Fatalf("cached replays consumed read budget: %d", state.ReadToolCalls)
	}
}

func TestCloudAgentCanvasWriteInvalidatesOnlyCanvasReadResults(t *testing.T) {
	state := &cloudAgentRuntime{
		ToolReadResults: map[string]cloudAgentCachedToolResult{
			`canvas_get_state:{}`:       {Result: json.RawMessage(`{"nodes":[]}`)},
			`canvas_read_storyboard:{}`: {Result: json.RawMessage(`{"shots":[]}`)},
			`skill_read_file:{}`:        {Result: json.RawMessage(`{"content":"skill"}`)},
			`model_list:{}`:             {Result: json.RawMessage(`{"models":[]}`)},
		},
		ToolReadReplays: map[string]int{
			`canvas_get_state:{}`:       2,
			`canvas_read_storyboard:{}`: 1,
			`skill_read_file:{}`:        3,
			`model_list:{}`:             4,
		},
	}

	cloudAgentInvalidateReadCache(state)
	if _, exists := state.ToolReadResults[`canvas_get_state:{}`]; exists {
		t.Fatal("canvas state cache survived a canvas write")
	}
	if _, exists := state.ToolReadResults[`canvas_read_storyboard:{}`]; exists {
		t.Fatal("storyboard cache survived a canvas write")
	}
	for _, key := range []string{`skill_read_file:{}`, `model_list:{}`} {
		if _, exists := state.ToolReadResults[key]; !exists {
			t.Fatalf("unrelated read cache %q was invalidated", key)
		}
	}
	if _, exists := state.ToolReadReplays[`canvas_get_state:{}`]; exists {
		t.Fatal("canvas replay count survived invalidation")
	}
	if state.ToolReadReplays[`skill_read_file:{}`] != 3 || state.ToolReadReplays[`model_list:{}`] != 4 {
		t.Fatalf("unrelated replay counts changed: %#v", state.ToolReadReplays)
	}
}

func TestCloudAgentReadToolCacheStopsCrossArgumentReadLoops(t *testing.T) {
	s, _, _, canvasID := cloudAgentProfileFixtureDB(t)
	state := &cloudAgentRuntime{Request: CloudAgentRequest{CanvasID: canvasID}}
	for index := 0; index < cloudAgentMaxReadToolCallsPerRun; index++ {
		call := cloudAgentCall{ID: "canvas-read"}
		call.Function.Name = "canvas_get_state"
		call.Function.Arguments = fmt.Sprintf(`{"offset":%d}`, index)
		if _, err := cloudAgentReadToolCached(s.repo, "user", state, call); err != nil {
			t.Fatalf("read %d unexpectedly failed before budget: %v", index+1, err)
		}
	}
	over := cloudAgentCall{ID: "canvas-read-over-budget"}
	over.Function.Name = "canvas_get_state"
	over.Function.Arguments = fmt.Sprintf(`{"offset":%d}`, cloudAgentMaxReadToolCallsPerRun)
	_, err := cloudAgentReadToolCached(s.repo, "user", state, over)
	var loopErr *cloudAgentReadLoopError
	if !errors.As(err, &loopErr) || !loopErr.Budget || loopErr.Count != cloudAgentMaxReadToolCallsPerRun+1 {
		t.Fatalf("expected cross-argument read budget guard, got %v", err)
	}
}

func TestCloudAgentReadCacheNormalizesObjectKeyOrder(t *testing.T) {
	state := &cloudAgentRuntime{Profile: cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "固定偏好"}}}}
	first := cloudAgentCall{ID: "profile-read-1"}
	first.Function.Name, first.Function.Arguments = "agent_profile_read", `{"scope":"user","unused":null}`
	second := first
	second.ID = "profile-read-2"
	second.Function.Arguments = `{"unused":null,"scope":"user"}`

	firstResult, firstErr := cloudAgentReadToolCached(nil, "user", state, first)
	if firstErr == nil || firstResult != nil {
		t.Fatalf("the first call should cache an argument error: result=%#v err=%v", firstResult, firstErr)
	}
	secondResult, secondErr := cloudAgentReadToolCached(nil, "user", state, second)
	if secondErr == nil || secondResult != nil || secondErr.Error() != firstErr.Error() {
		t.Fatalf("the second call should replay the cached argument error: result=%#v first=%v second=%v", secondResult, firstErr, secondErr)
	}
	if cloudAgentReadCacheKey(first) != cloudAgentReadCacheKey(second) {
		t.Fatalf("object key order changed the cache key: %q vs %q", cloudAgentReadCacheKey(first), cloudAgentReadCacheKey(second))
	}
}

func TestCloudAgentReadCacheBoundsEntriesAndBytes(t *testing.T) {
	state := &cloudAgentRuntime{
		ToolReadResults: make(map[string]cloudAgentCachedToolResult),
		ToolReadReplays: make(map[string]int),
	}
	body, err := json.Marshal(strings.Repeat("x", 64<<10))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < cloudAgentToolReadResultsMaxEntries+4; index++ {
		key := fmt.Sprintf("canvas_get_state:{\"offset\":%d}", index)
		state.ToolReadResults[key] = cloudAgentCachedToolResult{Result: body}
		state.ToolReadReplays[key] = index + 1
	}
	keep := fmt.Sprintf("canvas_get_state:{\"offset\":%d}", cloudAgentToolReadResultsMaxEntries+3)
	cloudAgentBoundToolReadResults(state, keep)
	if len(state.ToolReadResults) > cloudAgentToolReadResultsMaxEntries {
		t.Fatalf("read cache entry cap exceeded: %d", len(state.ToolReadResults))
	}
	if size := cloudAgentReadCacheJSONBytes(state.ToolReadResults, state.ToolReadReplays); size > cloudAgentToolReadResultsMaxBytes {
		t.Fatalf("read cache byte cap exceeded: %d", size)
	}
	if _, ok := state.ToolReadResults[keep]; !ok {
		t.Fatal("a fitting current read was evicted while bounding the cache")
	}
	for key := range state.ToolReadReplays {
		if _, ok := state.ToolReadResults[key]; !ok {
			t.Fatalf("orphan replay counter retained for evicted key %q", key)
		}
	}
}

func TestCloudAgentSaveBoundsLargeReadCache(t *testing.T) {
	req := agentTestRequest()
	state := &cloudAgentRuntime{
		Request:         req,
		Canonical:       canonicalAgentRequest{SystemPrompt: strings.Repeat("system ", 2000), Tools: cloudAgentTools(req)},
		Decisions:       map[string]string{},
		Events:          []CloudAgentEvent{},
		ToolReadResults: make(map[string]cloudAgentCachedToolResult),
		ToolReadReplays: make(map[string]int),
	}
	body, err := json.Marshal(strings.Repeat("canvas", 12<<10))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < cloudAgentToolReadResultsMaxEntries+4; index++ {
		key := fmt.Sprintf("canvas_get_state:{\"offset\":%d}", index)
		state.ToolReadResults[key] = cloudAgentCachedToolResult{Result: body}
		state.ToolReadReplays[key] = index + 1
	}
	run := &model.CloudAgentExecution{}
	if err := cloudAgentSave(run, state); err != nil {
		t.Fatalf("bounded cache checkpoint failed: %v", err)
	}
	if len(run.StateJSON) >= 512<<10 {
		t.Fatalf("bounded cache checkpoint still exceeds 512 KiB: %d", len(run.StateJSON))
	}
	var decoded cloudAgentRuntime
	if err := json.Unmarshal([]byte(run.StateJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ToolReadResults) > cloudAgentToolReadResultsMaxEntries || cloudAgentReadCacheJSONBytes(decoded.ToolReadResults, decoded.ToolReadReplays) > cloudAgentToolReadResultsMaxBytes {
		t.Fatalf("persisted read cache is unbounded: entries=%d bytes=%d", len(decoded.ToolReadResults), cloudAgentReadCacheJSONBytes(decoded.ToolReadResults, decoded.ToolReadReplays))
	}
}

func TestCloudAgentReadCacheReplaysAfterContextCompaction(t *testing.T) {
	state := &cloudAgentRuntime{
		Profile: cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "固定偏好"}}},
		Canonical: canonicalAgentRequest{Messages: []map[string]any{
			{"role": "tool", "content": `{"scope":"user","content":"固定偏好"}`},
		}},
	}
	call := cloudAgentCall{ID: "profile-read"}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"user"}`
	if _, err := cloudAgentReadToolCached(nil, "user", state, call); err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	state.Canonical.Messages = []map[string]any{
		{"role": "user", "content": "压缩后的检查点"},
	}
	replayed, err := cloudAgentReadToolCached(nil, "user", state, call)
	if err != nil {
		t.Fatalf("replay after context compaction failed: %v", err)
	}
	if got := replayed.(map[string]any)["content"]; got != "固定偏好" {
		t.Fatalf("compaction replay lost the cached body: %#v", replayed)
	}
	if _, err := cloudAgentReadToolCached(nil, "user", state, call); err == nil {
		t.Fatal("context-compaction recovery must not reopen an unlimited replay loop")
	}
}

func TestCloudAgentReadCacheBoundsReplayMetadataAndPreservesProfile(t *testing.T) {
	profileKey := `agent_profile_read:{"scope":"user"}`
	state := &cloudAgentRuntime{
		Profile: cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "固定偏好"}}},
		ToolReadResults: map[string]cloudAgentCachedToolResult{
			profileKey: {Result: json.RawMessage(`{"scope":"user","content":"固定偏好"}`)},
		},
		ToolReadReplays: map[string]int{profileKey: 1},
	}
	for index := 0; index < cloudAgentToolReadResultsMaxEntries; index++ {
		key := fmt.Sprintf("canvas_get_state:%s-%d", strings.Repeat("k", 20<<10), index)
		state.ToolReadResults[key] = cloudAgentCachedToolResult{Result: json.RawMessage(`{"nodes":[]}`)}
		state.ToolReadReplays[key] = index + 1
	}
	if cloudAgentReadResultsJSONBytes(state.ToolReadResults) >= cloudAgentToolReadResultsMaxBytes {
		t.Fatal("test fixture must fit the result-only budget before replay metadata is counted")
	}
	if cloudAgentReadCacheJSONBytes(state.ToolReadResults, state.ToolReadReplays) <= cloudAgentToolReadResultsMaxBytes {
		t.Fatal("test fixture must exceed the combined result/replay budget")
	}

	cloudAgentBoundToolReadResults(state, "")
	if _, ok := state.ToolReadResults[profileKey]; !ok {
		t.Fatal("profile cache entry was evicted")
	}
	if state.ToolReadReplays[profileKey] != 1 {
		t.Fatalf("profile replay counter was evicted or changed: %#v", state.ToolReadReplays)
	}
	if size := cloudAgentReadCacheJSONBytes(state.ToolReadResults, state.ToolReadReplays); size > cloudAgentToolReadResultsMaxBytes {
		t.Fatalf("combined read cache budget exceeded: %d", size)
	}
	for key := range state.ToolReadReplays {
		if _, ok := state.ToolReadResults[key]; !ok {
			t.Fatalf("orphan replay counter retained for evicted key %q", key)
		}
	}
}

func TestCloudAgentReadCacheDropsTransientErrorAndReplayCounter(t *testing.T) {
	call := cloudAgentCall{ID: "profile-read"}
	call.Function.Name, call.Function.Arguments = "agent_profile_read", `{"scope":"user"}`
	key := cloudAgentReadCacheKey(call)
	state := &cloudAgentRuntime{
		Profile:         cloudAgentProfileSnapshot{Layers: []AgentProfileLayer{{Scope: model.AgentProfileScopeUser, Content: "固定偏好"}}},
		ToolReadResults: map[string]cloudAgentCachedToolResult{key: {Error: "暂时不可用"}},
		ToolReadReplays: map[string]int{key: 4},
	}

	result, err := cloudAgentReadToolCached(nil, "user", state, call)
	if err != nil {
		t.Fatalf("transient cached error was not retried: %v", err)
	}
	if result.(map[string]any)["content"] != "固定偏好" {
		t.Fatalf("retry did not return the live profile: %#v", result)
	}
	if state.ToolReadReplays[key] != 0 {
		t.Fatalf("transient error replay counter was retained: %#v", state.ToolReadReplays)
	}
}

func TestCloudAgentSaveFailureDoesNotMutateLiveReadCache(t *testing.T) {
	state := &cloudAgentRuntime{
		ToolReadResults: make(map[string]cloudAgentCachedToolResult),
		ToolReadReplays: make(map[string]int),
	}
	for index := 0; index < cloudAgentToolReadResultsMaxEntries+1; index++ {
		key := fmt.Sprintf("canvas_get_state:{\"offset\":%d}", index)
		state.ToolReadResults[key] = cloudAgentCachedToolResult{Result: json.RawMessage(`{`)}
		state.ToolReadReplays[key] = index + 1
	}
	run := &model.CloudAgentExecution{}
	if err := cloudAgentSave(run, state); err == nil {
		t.Fatal("invalid cached JSON unexpectedly saved")
	}
	if len(state.ToolReadResults) != cloudAgentToolReadResultsMaxEntries+1 || len(state.ToolReadReplays) != cloudAgentToolReadResultsMaxEntries+1 {
		t.Fatalf("failed save polluted live read cache: results=%d replays=%d", len(state.ToolReadResults), len(state.ToolReadReplays))
	}
	for key, count := range state.ToolReadReplays {
		if _, ok := state.ToolReadResults[key]; !ok || count < 1 {
			t.Fatalf("failed save changed live cache entry %q: %#v / %#v", key, state.ToolReadResults[key], state.ToolReadReplays)
		}
	}
}
