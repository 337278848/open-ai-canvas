package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/canvas/capability"
	"infinite-canvas/backend/internal/model"
)

func TestCloudAgentMixedCanvasReadsUnsupportedNodesWithoutGrantingCapabilities(t *testing.T) {
	nodes := []map[string]any{{"id": "text", "type": "text", "metadata": map[string]any{"content": "readable"}}}
	for _, kind := range []string{"ai-art-critique", "config", "drawing", "future-plugin"} {
		nodes = append(nodes, map[string]any{"id": kind, "type": kind, "title": "插件节点", "position": map[string]any{"x": 10.0, "y": 20.0}, "metadata": map[string]any{"content": "PRIVATE_SENTINEL", "apiKey": "PRIVATE_SENTINEL"}})
		if _, supported := cloudAgentNodeCapabilityForType(kind); supported {
			t.Fatalf("unsupported type gained write capability: %s", kind)
		}
		if err := validateCreationOps([]CreationCanvasOp{{Type: "add_node", ID: "new", NodeType: kind}}); err == nil {
			t.Fatalf("unsupported creation accepted: %s", kind)
		}
		if _, _, err := cloudAgentReferenceDescriptor(nodes[len(nodes)-1]); err == nil {
			t.Fatalf("unsupported media reference accepted: %s", kind)
		}
		if err := validateCloudAgentConnection(nodes, kind, "text"); err == nil {
			t.Fatalf("unsupported connection accepted: %s", kind)
		}
	}
	doc := map[string]any{"nodes": nodes, "connections": []map[string]any{{"id": "edge", "fromNodeId": "drawing", "toNodeId": "text"}}}
	raw, _ := json.Marshal(doc)
	summary, err := cloudAgentCanvasSummary(&model.CanvasProject{PayloadJSON: string(raw)})
	if err != nil || strings.Contains(summary, "PRIVATE_SENTINEL") || !strings.Contains(summary, `"agentSupported":false`) {
		t.Fatalf("mixed summary failed or leaked metadata: %v", err)
	}
	for _, ids := range [][]string{nil, {"drawing", "config"}} {
		view, err := cloudAgentCanvasState(nil, "user", "agent-canvas", doc, 0, ids, 0)
		if err != nil {
			t.Fatal(err)
		}
		result := view.(map[string]any)
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "PRIVATE_SENTINEL") || result["snapshotHash"] != cloudAgentCanvasHash(doc) || len(result["connections"].([]any)) != 1 {
			t.Fatal("unsafe projection or lost snapshot/connection")
		}
		want := len(nodes)
		if ids != nil {
			want = len(ids)
		}
		if len(result["nodes"].([]any)) != want {
			t.Fatal("nodes silently omitted")
		}
	}
}

func TestCloudAgentCanvasSummaryIsACatalogNotNodeBodies(t *testing.T) {
	body := strings.Repeat("镜", 4000)
	nodes := make([]map[string]any, 0, 200)
	for index := 0; index < 200; index++ {
		nodes = append(nodes, map[string]any{
			"id":    fmt.Sprintf("node-%d", index),
			"type":  "text",
			"title": fmt.Sprintf("镜头 %d %s", index, strings.Repeat("标题", 40)),
			"metadata": map[string]any{
				"content": body, "prompt": "PRIVATE_PROMPT", "apiKey": "PRIVATE_SENTINEL",
			},
		})
	}
	raw, err := json.Marshal(map[string]any{"nodes": nodes})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := cloudAgentCanvasSummary(&model.CanvasProject{Title: "大画布", PayloadJSON: string(raw)})
	if err != nil {
		t.Fatalf("large canvas catalog rejected: %v", err)
	}
	if strings.Contains(summary, body[:40]) || strings.Contains(summary, "PRIVATE_PROMPT") || strings.Contains(summary, "PRIVATE_SENTINEL") || strings.Contains(summary, `"content"`) {
		t.Fatal("catalog leaked node bodies or metadata")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["kind"] != "node_catalog" || parsed["totalNodes"] != float64(200) {
		t.Fatalf("catalog identity = %+v", parsed)
	}
	included, _ := parsed["includedNodes"].(float64)
	omitted, _ := parsed["omittedNodes"].(float64)
	if included <= 0 || omitted <= 0 || int(included+omitted) != 200 || included > float64(cloudAgentCanvasSummaryMaxNodes) {
		t.Fatalf("expected a bounded catalog, included=%v omitted=%v", included, omitted)
	}
	if len(summary) > cloudAgentCanvasSummaryBudgetBytes+4096 {
		t.Fatalf("catalog still carries canvas-sized payload: %d", len(summary))
	}
}

func TestCloudAgentCanvasSummaryUsesSelectedNodeNeighborhood(t *testing.T) {
	doc := map[string]any{
		"nodes": []map[string]any{
			{"id": "focus", "type": "text", "title": "主体"},
			{"id": "neighbor", "type": "image", "title": "关联素材"},
			{"id": "unrelated", "type": "text", "title": "无关内容"},
		},
		"connections": []map[string]any{{"fromNodeId": "focus", "toNodeId": "neighbor"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := cloudAgentCanvasSummary(&model.CanvasProject{PayloadJSON: string(raw)}, "focus")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(summary), &result); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result["nodes"])
	if strings.Contains(string(encoded), "unrelated") || !strings.Contains(string(encoded), "focus") || !strings.Contains(string(encoded), "neighbor") {
		t.Fatalf("focused catalog did not include exactly the local graph neighborhood: %s", encoded)
	}
	selection := result["selection"].(map[string]any)
	if selection["includedNeighbors"] != float64(1) || selection["nextReadDepth"] != float64(1) {
		t.Fatalf("focused catalog metadata is incorrect: %+v", selection)
	}
	if _, err := cloudAgentCanvasSummary(&model.CanvasProject{PayloadJSON: string(raw)}, "deleted"); err == nil {
		t.Fatal("stale selected node was accepted")
	}
}

func TestCloudAgentToolsFollowCanvasCapabilityRegistry(t *testing.T) {
	req := agentTestRequest()
	req.PermissionMode = "auto"
	req.ContextScope = []string{"canvas"}
	req.Budget.MaxGenerationTasks = 1
	functions := map[string]map[string]any{}
	for _, tool := range cloudAgentTools(req) {
		function := tool["function"].(map[string]any)
		functions[function["name"].(string)] = function["parameters"].(map[string]any)
	}
	stateProperties := functions["canvas_get_state"]["properties"].(map[string]any)
	maxItems := stateProperties["maxItems"].(map[string]any)
	if maxItems["type"] != "integer" || maxItems["minimum"] != 1 || maxItems["maximum"] != 40 {
		t.Fatalf("canvas_get_state maxItems contract drifted: %#v", maxItems)
	}
	apply := functions["canvas_apply_ops"]
	if apply == nil {
		t.Fatal("canvas write tool is missing")
	}
	ops := apply["properties"].(map[string]any)["ops"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if got := ops["nodeType"].(map[string]any)["enum"]; !reflect.DeepEqual(got, canvasCapabilityRegistry.Types()) {
		t.Fatalf("node types differ from registry: %v", got)
	}
	patch := ops["patch"].(map[string]any)["properties"].(map[string]any)
	nodeTypes := cloudAgentNodeTypes()["nodes"].([]map[string]any)
	nodeTypeDefinitions := map[string]map[string]any{}
	for _, item := range nodeTypes {
		nodeTypeDefinitions[item["type"].(string)] = item
	}
	for _, descriptor := range canvasCapabilityRegistry.List() {
		if !descriptor.CanUpdate && len(descriptor.PatchFields) != 0 {
			t.Fatalf("non-updateable capability %s declares dead patch fields", descriptor.Type)
		}
		for key, field := range descriptor.PatchFields {
			property, ok := patch[key].(map[string]any)
			if !ok || property["type"] != field.Kind {
				t.Fatalf("patch field %s (%s) missing from tool schema", key, descriptor.Type)
			}
			definitions, _ := nodeTypeDefinitions[descriptor.Type]["updateFields"].(map[string]any)
			definition, _ := definitions[key].(map[string]any)
			if field.Label == "" || definition["label"] != field.Label {
				t.Fatalf("patch field %s (%s) has no stable user-facing label", key, descriptor.Type)
			}
		}
	}
	if got := functions["generate_media"]["properties"].(map[string]any)["mode"].(map[string]any)["enum"]; !reflect.DeepEqual(got, cloudAgentGenerationModeNames()) {
		t.Fatalf("generation modes differ from implemented adapters: %v", got)
	}
	if got := CloudAgentCapabilitySetInfo(); got.Hash != canvasCapabilityRegistry.Hash() || got.Version != capability.SetVersion || !reflect.DeepEqual(got.Nodes, canvasCapabilityRegistry.Types()) {
		t.Fatalf("capability endpoint info differs from registry: %+v", got)
	}
}

func TestCloudAgentImageEditingToolsRespectPermissionBoundaries(t *testing.T) {
	readOnly := agentTestRequest()
	readOnly.PermissionMode = "read_only"
	readOnly.ContextScope = []string{"canvas"}
	readNames := map[string]bool{}
	for _, tool := range cloudAgentTools(readOnly) {
		readNames[tool["function"].(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"image_text_detect", "image_annotation_render"} {
		if !readNames[name] {
			t.Fatalf("read-only tool %s missing", name)
		}
	}
	if readNames["image_layer_split"] {
		t.Fatal("image_layer_split must not be exposed in read-only mode")
	}

	write := readOnly
	write.PermissionMode = "auto"
	write.Budget.MaxGenerationTasks = 1
	writeNames := map[string]bool{}
	for _, tool := range cloudAgentTools(write) {
		writeNames[tool["function"].(map[string]any)["name"].(string)] = true
	}
	if !writeNames["image_layer_split"] || !cloudAgentWrite("image_layer_split") {
		t.Fatal("image_layer_split must be an approved write tool")
	}
	if got := cloudAgentMediaCall(cloudAgentCall{Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "image_layer_split", Arguments: `{"prompt":"split"}`}}); got.Function.Name != "image_layer_split" || !strings.Contains(got.Function.Arguments, `"mode":"image"`) {
		t.Fatalf("image layer split was not normalized to image media: %+v", got.Function)
	}
}

func TestCloudAgentAnnotationRenderFeedsControlledTransientReference(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	canvas := &model.CanvasProject{ID: "annotation-canvas", UserID: "user", PayloadJSON: `{"nodes":[{"id":"image-1","type":"image","title":"原图","width":640,"height":480,"metadata":{"content":"saved"}}],"connections":[]}`}
	if err := db.Create(canvas).Error; err != nil {
		t.Fatal(err)
	}
	state := &cloudAgentRuntime{RuntimeRunID: "annotation-run", Request: CloudAgentRequest{CanvasID: canvas.ID}, TransientReferences: map[string]cloudAgentTransientReference{}}
	call := cloudAgentCall{ID: "call-annotation"}
	call.Function.Name = "image_annotation_render"
	call.Function.Arguments = `{"nodeId":"image-1","annotations":[{"label":"主体","x":0.5,"y":0.5}]}`
	result, err := cloudAgentReadTool(s.repo, "user", state, call, s)
	if err != nil {
		t.Fatal(err)
	}
	view := result.(map[string]any)
	refID := stringValue(view["referenceTransientId"])
	ref, ok := state.TransientReferences[refID]
	if !ok || ref.ResourceID == "" || ref.ExpiresAt.IsZero() {
		t.Fatalf("annotation transient reference missing or unsafe: %#v", state.TransientReferences)
	}
	resultJSON, _ := json.Marshal(result)
	if !strings.Contains(string(resultJSON), "image/png") {
		t.Fatalf("annotation result must advertise PNG reference: %s", resultJSON)
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	args := cloudAgentMediaArgs{Mode: "image", Prompt: "按标注编辑", SnapshotHash: cloudAgentMediaContentHash(doc), NodeID: "result-image", Title: "编辑结果", Size: "1:1", ReferenceTransientIDs: []string{refID}}
	_, _, refs, err := cloudAgentMediaDocument(s.repo, "user", canvas.ID, args, state.TransientReferences)
	if err != nil {
		t.Fatal(err)
	}
	images, ok := refs["referenceImages"].([]any)
	if !ok || len(images) != 1 || stringValue(images[0].(map[string]any)["id"]) != refID {
		t.Fatalf("controlled transient reference was not forwarded: %#v", refs)
	}
}

func TestCloudAgentPolicyPublishesSkillManifestWithoutInliningSkillBody(t *testing.T) {
	skill := cloudAgentSkill{ID: "skill-1", Name: "任务技能", Description: "当用户要写短剧剧本时调用", Version: "v1", Hash: agentProfileHash("skill"), Instruction: "PRIVATE_SKILL_BODY", Files: map[string]string{"references/a.md": "A"}}
	text, _, err := compileCloudAgentPolicies(agentTestRequest(), []cloudAgentSkill{skill}, "", cloudAgentProfileSnapshot{Revision: agentProfileRevision(nil), Hash: agentProfileHash("")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, skill.Instruction) || !strings.Contains(text, `"entryPath":"SKILL.md"`) || !strings.Contains(text, `"files":["SKILL.md","references/a.md"]`) || !strings.Contains(text, `"description":"当用户要写短剧剧本时调用"`) {
		t.Fatalf("compiled policy did not publish a safe on-demand skill manifest: %s", text)
	}
}

func TestCloudAgentPolicyTruncatesOversizedSkillDescription(t *testing.T) {
	long := strings.Repeat("描", 600)
	skill := cloudAgentSkill{ID: "skill-2", Name: "长描述技能", Description: long, Version: "v1", Hash: agentProfileHash("skill2")}
	text, _, err := compileCloudAgentPolicies(agentTestRequest(), []cloudAgentSkill{skill}, "", cloudAgentProfileSnapshot{Revision: agentProfileRevision(nil), Hash: agentProfileHash("")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, long) || !strings.Contains(text, strings.Repeat("描", 500)+"…") {
		t.Fatalf("oversized skill description was not capped at 500 runes: %s", text)
	}
}

func TestCloudAgentPolicyPublishesCapabilityRoutingGuide(t *testing.T) {
	text, _, err := compileCloudAgentPolicies(agentTestRequest(), nil, "", cloudAgentProfileSnapshot{Revision: agentProfileRevision(nil), Hash: agentProfileHash("")})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"节点能力速查",
		"由服务端能力注册表生成",
		"分镜脚本（script）",
		"多镜头",
		"逐镜审查",
		"后续维护",
		"单画面、一次性说明或快速试验优先轻量节点",
		"普通文本或 Markdown 不能伪装成结构化分镜",
		"不为形式强制使用任何节点",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("compiled policy omitted capability routing guidance %q: %s", expected, text)
		}
	}
}

func TestCloudAgentGenerationAdapterRejectsUnimplementedMode(t *testing.T) {
	original := canvasCapabilityRegistry
	defer func() { canvasCapabilityRegistry = original }()
	descriptors := original.List()
	descriptors = append(descriptors, capability.Descriptor{
		Type: "table", Version: "1", Label: "多维表格", DefaultWidth: 640, DefaultHeight: 400,
		InputKind: "table_data", GenerationMode: "table-render",
	})
	registry, err := capability.NewRegistry(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	canvasCapabilityRegistry = registry
	if descriptor, ok := cloudAgentNodeCapabilityForGenerationMode("table-render"); !ok || descriptor.Type != "table" {
		t.Fatal("hypothetical new mode did not resolve its canvas descriptor")
	}
	if cloudAgentGenerationModeSupported("table-render") || cloudAgentMediaOperation("table-render", nil) != "" {
		t.Fatal("unimplemented mode may create a billable task")
	}
	for _, mode := range cloudAgentGenerationModeNames() {
		if mode == "table-render" {
			t.Fatal("unimplemented billing mode appeared in the tool schema")
		}
	}
	if err := validateCloudAgentMediaReferences("table-render", nil); err == nil {
		t.Fatal("unimplemented mode accepted media references")
	}
	for _, mode := range cloudAgentGenerationModeNames() {
		if _, ok := cloudAgentNodeCapabilityForGenerationMode(mode); !ok || cloudAgentMediaOperation(mode, nil) == "" {
			t.Fatalf("exposed mode %s lacks a node or task adapter", mode)
		}
	}
}

func TestCloudAgentProfileProjectScopeDoesNotSilentlyDiscardCanvas(t *testing.T) {
	if err := validateAgentProfileScope(AgentProfileRequest{Scope: "project", ProjectID: "p1", CanvasID: "c1"}); err == nil {
		t.Fatal("project profile accepted a canvas ID that would be silently discarded")
	}
}

func TestCloudAgentProjectionRejectsMissingAdapterAndOpaqueMetadata(t *testing.T) {
	node := map[string]any{"id": "n1", "type": "text", "title": "镜头"}
	meta := map[string]any{"content": "公开正文", "storageKey": "resource:private", "url": "https://private.example/test", "status": "idle"}
	descriptor, _ := cloudAgentNodeCapabilityForType("text")
	projected, err := cloudAgentProjectNodeFields(node, meta, descriptor, descriptor.DetailFields, 16000, true, 0)
	if err != nil || projected["content"] != "公开正文" || projected["storageKey"] != nil || projected["url"] != nil {
		t.Fatalf("unsafe or incomplete projection: %v, %v", projected, err)
	}
	descriptor.ProjectionField = "storyboard"
	descriptor.ProjectionKind = "unregistered-projector"
	descriptor.DetailFields = []string{"storyboard"}
	meta["storyboard"] = map[string]any{"rows": []any{}}
	if _, err := cloudAgentProjectNodeFields(node, meta, descriptor, descriptor.DetailFields, 16000, true, 0); err == nil {
		t.Fatal("unregistered structured projector must fail closed")
	}
}

func TestCloudAgentCanvasStateDoesNotForwardUnknownObjectFields(t *testing.T) {
	doc := map[string]any{"nodes": []map[string]any{{
		"id": "safe", "type": "text", "title": "镜头",
		"position": map[string]any{"x": 10.0, "y": 20.0, "storageKey": "resource:secret"},
		"width":    200.0, "metadata": map[string]any{"status": map[string]any{"url": "https://secret.invalid"}, "content": "画面内容"},
	}}}
	view, err := cloudAgentCanvasState(nil, "user", "agent-canvas", doc, 0, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := view.(map[string]any)["nodes"].([]any)[0].(map[string]any)
	if item["status"] != nil || item["position"].(map[string]any)["storageKey"] != nil || item["content"] != "画面内容" {
		t.Fatalf("unsafe object fields leaked into model context: %v", item)
	}
}

func TestCloudAgentCanvasStateHonorsRequestedSummarySize(t *testing.T) {
	nodes := make([]map[string]any, 0, 10)
	for index := 0; index < 10; index++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("node-%d", index), "type": "text", "title": "节点"})
	}
	doc := map[string]any{"nodes": nodes}
	view, err := cloudAgentCanvasState(nil, "user", "agent-canvas", doc, 0, nil, 0, cloudAgentCanvasReadOptions{MaxItems: 8})
	if err != nil {
		t.Fatal(err)
	}
	result := view.(map[string]any)
	if got := len(result["nodes"].([]any)); got != 8 {
		t.Fatalf("summary node count = %d, want 8", got)
	}
	if result["nextOffset"] != 8 || result["hasMore"] != true {
		t.Fatalf("summary pagination = %#v, want nextOffset=8 and hasMore=true", result)
	}
}

func TestCloudAgentDurablePolicySnapshotRejectsMissingOrUnsupportedContracts(t *testing.T) {
	_, snapshot, err := compileCloudAgentPolicies(agentTestRequest(), nil, "", cloudAgentProfileSnapshot{Revision: agentProfileRevision(nil), Hash: agentProfileHash("")})
	if err != nil || validateCloudAgentPolicySnapshot(snapshot) != nil {
		t.Fatalf("valid policy snapshot rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*cloudAgentPolicySnapshot)
	}{
		{"missing compiler", func(p *cloudAgentPolicySnapshot) { p.CompilerVersion = "" }},
		{"unsupported compiler", func(p *cloudAgentPolicySnapshot) { p.CompilerVersion = "future" }},
		{"missing system id", func(p *cloudAgentPolicySnapshot) { p.SystemPolicyID = "" }},
		{"missing system hash", func(p *cloudAgentPolicySnapshot) { p.SystemPolicyHash = "" }},
		{"missing media id", func(p *cloudAgentPolicySnapshot) { p.MediaPolicyID = "" }},
		{"missing media hash", func(p *cloudAgentPolicySnapshot) { p.MediaPolicyHash = "" }},
		{"missing capability version", func(p *cloudAgentPolicySnapshot) { p.CapabilitySetVersion = "" }},
		{"missing capability hash", func(p *cloudAgentPolicySnapshot) { p.CapabilitySetHash = "" }},
		{"invalid reasoning", func(p *cloudAgentPolicySnapshot) { p.ReasoningMode = "enabled" }},
		{"missing profile revision", func(p *cloudAgentPolicySnapshot) { p.ProfileRevision = "" }},
		{"invalid profile hash", func(p *cloudAgentPolicySnapshot) { p.ProfileHash = "not-sha256" }},
		{"changed system contents", func(p *cloudAgentPolicySnapshot) { p.SystemPolicyHash = agentProfileHash("different system") }},
		{"changed media contents", func(p *cloudAgentPolicySnapshot) { p.MediaPolicyHash = agentProfileHash("different media") }},
		{"changed canvas contract", func(p *cloudAgentPolicySnapshot) { p.CapabilitySetHash = agentProfileHash("different capabilities") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := snapshot
			test.mutate(&corrupt)
			if err := validateCloudAgentPolicySnapshot(corrupt); err == nil {
				t.Fatal("corrupted durable policy snapshot was accepted")
			}
		})
	}
}

func TestCloudAgentCanvasStateUsesStableUnifiedSubgraphLimit(t *testing.T) {
	nodes := make([]map[string]any, 0, cloudAgentRelatedNodeLimit+33)
	edges := make([]map[string]any, 0, cloudAgentRelatedNodeLimit+32)
	for index := 0; index < cloudAgentRelatedNodeLimit+32; index++ {
		id := fmt.Sprintf("child-%03d", index)
		nodes = append(nodes, map[string]any{"id": id, "type": "text", "title": id})
		edges = append(edges, map[string]any{
			"id":         fmt.Sprintf("edge-%03d", index),
			"fromNodeId": "focus",
			"toNodeId":   id,
		})
	}
	// Put the focus node at the end of the document to prove that selection
	// order, rather than canvas storage order, gives focus priority.
	nodes = append(nodes, map[string]any{"id": "focus", "type": "text", "title": "焦点"})
	doc := map[string]any{"nodes": nodes, "connections": edges}

	tests := []struct {
		name string
		read func(int) (any, error)
	}{
		{
			name: "bounded depth",
			read: func(offset int) (any, error) {
				return cloudAgentCanvasStateWithFocus(nil, "user", "canvas", doc, offset, []string{"focus"}, 1, 0)
			},
		},
		{
			name: "full related component",
			read: func(offset int) (any, error) {
				return cloudAgentCanvasStateWithRelated(nil, "user", "canvas", doc, offset, []string{"focus"}, 0)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first, err := test.read(0)
			if err != nil {
				t.Fatal(err)
			}
			second, err := test.read(0)
			if err != nil {
				t.Fatal(err)
			}
			firstResult := first.(map[string]any)
			secondResult := second.(map[string]any)
			selection, ok := firstResult["selection"].(map[string]any)
			if !ok {
				t.Fatalf("missing selection metadata: %#v", firstResult)
			}
			if selection["selectedNodes"] != cloudAgentRelatedNodeLimit ||
				selection["nodeLimit"] != cloudAgentRelatedNodeLimit ||
				selection["truncated"] != true {
				t.Fatalf("selection did not apply the unified hard limit: %#v", selection)
			}
			nodes, ok := firstResult["nodes"].([]any)
			if !ok || len(nodes) != cloudAgentRelatedNodeLimit {
				t.Fatalf("bounded read returned %d nodes, want %d", len(nodes), cloudAgentRelatedNodeLimit)
			}
			if nodes[0].(map[string]any)["id"] != "focus" {
				t.Fatalf("focus node was not prioritized: %#v", nodes[0])
			}
			firstIDs := make([]string, 0, len(nodes))
			secondIDs := make([]string, 0, len(secondResult["nodes"].([]any)))
			for _, raw := range nodes {
				firstIDs = append(firstIDs, stringValue(raw.(map[string]any)["id"]))
			}
			for _, raw := range secondResult["nodes"].([]any) {
				secondIDs = append(secondIDs, stringValue(raw.(map[string]any)["id"]))
			}
			if !reflect.DeepEqual(firstIDs, secondIDs) {
				t.Fatalf("bounded selection was not stable: first=%v second=%v", firstIDs, secondIDs)
			}
		})
	}
}

func TestCloudAgentCanvasStateKeepsCrossPageRelatedConnectionsReadable(t *testing.T) {
	const nodeCount = cloudAgentRelatedNodeLimit
	nodes := make([]map[string]any, 0, nodeCount)
	edges := make([]map[string]any, 0, nodeCount)
	for index := 0; index < nodeCount; index++ {
		id := fmt.Sprintf("node-%03d", index)
		nodes = append(nodes, map[string]any{
			"id":    id,
			"type":  "text",
			"title": strings.Repeat("x", 300),
		})
		if index > 0 {
			edges = append(edges, map[string]any{
				"id":         fmt.Sprintf("chain-%03d", index),
				"fromNodeId": fmt.Sprintf("node-%03d", index-1),
				"toNodeId":   id,
			})
		}
	}
	// This edge joins endpoints that are guaranteed to be far apart in the
	// node-page order. The complete related selection still contains both.
	edges = append(edges, map[string]any{
		"id": "cross-page", "fromNodeId": "node-000", "toNodeId": "node-255",
	})
	doc := map[string]any{"nodes": nodes, "connections": edges}

	seenNodes := map[string]bool{}
	seenConnections := map[string]bool{}
	sawNodePagination, sawConnectionPagination := false, false
	offset, connectionOffset := 0, 0
	for attempt := 0; attempt < 100; attempt++ {
		view, err := cloudAgentCanvasStateWithRelated(nil, "user", "canvas", doc, offset, []string{"node-000"}, 0, connectionOffset)
		if err != nil {
			t.Fatal(err)
		}
		result := view.(map[string]any)
		for _, raw := range result["nodes"].([]any) {
			seenNodes[stringValue(raw.(map[string]any)["id"])] = true
		}
		for _, raw := range result["connections"].([]any) {
			seenConnections[stringValue(raw.(map[string]any)["id"])] = true
		}

		if result["hasMoreConnections"] == true {
			sawConnectionPagination = true
			nextConnection, ok := cloudAgentInteger(result["nextConnectionOffset"])
			if !ok || nextConnection <= connectionOffset {
				t.Fatalf("connection cursor did not advance: %#v", result)
			}
			connectionOffset = nextConnection
			continue
		}
		if result["hasMore"] == true {
			sawNodePagination = true
			nextOffset, ok := cloudAgentInteger(result["nextOffset"])
			if !ok || nextOffset <= offset {
				t.Fatalf("node cursor did not advance: %#v", result)
			}
			offset, connectionOffset = nextOffset, 0
			continue
		}
		break
	}

	if len(seenNodes) != nodeCount {
		t.Fatalf("node pagination omitted related nodes: got %d, want %d", len(seenNodes), nodeCount)
	}
	if !sawNodePagination || !sawConnectionPagination {
		t.Fatalf("fixture did not exercise both cursors: node=%v connection=%v", sawNodePagination, sawConnectionPagination)
	}
	if !seenConnections["cross-page"] {
		t.Fatalf("cross-page endpoint connection was lost; seen=%v", seenConnections)
	}
	if len(seenConnections) != len(edges) {
		t.Fatalf("connection pagination omitted edges: got %d, want %d", len(seenConnections), len(edges))
	}
}

func TestCloudAgentCanvasStateDoesNotSilentlyCombineMaxItemsWithSubgraph(t *testing.T) {
	doc := map[string]any{
		"nodes": []map[string]any{
			{"id": "focus", "type": "text", "title": "焦点"},
			{"id": "child", "type": "text", "title": "子节点"},
		},
		"connections": []map[string]any{{"id": "edge", "fromNodeId": "focus", "toNodeId": "child"}},
	}
	if _, err := cloudAgentCanvasState(nil, "user", "canvas", doc, 0, nil, 0); err != nil {
		t.Fatalf("omitted maxItems must retain the legacy default: %v", err)
	}
	if _, err := cloudAgentCanvasStateSelectedWithOptions(nil, "user", "canvas", doc, 0, nil, []string{"focus"}, 1, false, 0, 8, 0); err == nil || !strings.Contains(err.Error(), "maxItems") {
		t.Fatalf("non-default maxItems must be rejected for a subgraph read, got %v", err)
	}
	if _, err := cloudAgentCanvasStateSelectedWithOptions(nil, "user", "canvas", doc, 0, nil, []string{"focus"}, 1, false, 0, 0, 0); err == nil {
		t.Fatal("zero maxItems must not be accepted by the selected state boundary")
	}
}

func TestCloudAgentCanvasStatePreservesNodeIDReadOffsetSemantics(t *testing.T) {
	doc := map[string]any{
		"nodes": []map[string]any{
			{"id": "first", "type": "text", "title": "第一个"},
			{"id": "wanted", "type": "text", "title": "目标"},
		},
	}
	view, err := cloudAgentCanvasState(nil, "user", "canvas", doc, 1, []string{"wanted"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	nodes := view.(map[string]any)["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["id"] != "wanted" {
		t.Fatalf("nodeIds read unexpectedly applied summary offset: %#v", nodes)
	}
}
