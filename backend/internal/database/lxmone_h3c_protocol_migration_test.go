package database

import (
	"encoding/json"
	"testing"

	"yingce/backend/internal/model"
)

func TestMigrateLXMoneH3CRoutesMisassignedMaxProtocol(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-lxmone-h3c-protocol?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ChannelModel{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	legacy := `{"version":1,"video":{"duration":{"selection":"range","min":1,"max":15,"step":1,"default":6},"resolutions":["480p","768p"],"defaultResolution":"768p","references":{"maxImages":12,"maxVideos":12,"maxAudios":12}}}`
	rows := []model.ChannelModel{
		{ID: "h3-c", ModelKey: "minimax-h3-c", ProviderModelKey: "minimax-h3-c", Capability: "video", Protocol: "lxmone-h3-max-videos", CapabilityConfigJSON: legacy, CapabilityVersion: 3},
		{ID: "h3-c-prefixed", ModelKey: "models/minimax-h3-c", ProviderModelKey: "models/minimax-h3-c", Capability: "video", Protocol: "lxmone-h3-max-videos", CapabilityConfigJSON: legacy, CapabilityVersion: 1},
		{ID: "h3-max", ModelKey: "minimax-h3-max", ProviderModelKey: "minimax-h3-max", Capability: "video", Protocol: "lxmone-h3-max-videos", CapabilityConfigJSON: legacy, CapabilityVersion: 2},
		{ID: "h3-a", ModelKey: "minimax-h3-a", ProviderModelKey: "minimax-h3-a", Capability: "video", Protocol: "lxmone-h3-workflow", CapabilityConfigJSON: legacy, CapabilityVersion: 4},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	if err := migrateLXMoneH3CProtocol(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateLXMoneH3CProtocol(db); err != nil {
		t.Fatal(err)
	}

	var migrated model.ChannelModel
	if err := db.First(&migrated, "id = ?", "h3-c").Error; err != nil {
		t.Fatal(err)
	}
	if migrated.Protocol != "lxmone-h3-workflow" || migrated.CapabilityVersion != 4 {
		t.Fatalf("migrated protocol/version = %s/%d", migrated.Protocol, migrated.CapabilityVersion)
	}
	assertH3CCapabilityJSON(t, migrated.CapabilityConfigJSON)

	var prefixed model.ChannelModel
	if err := db.First(&prefixed, "id = ?", "h3-c-prefixed").Error; err != nil {
		t.Fatal(err)
	}
	if prefixed.Protocol != "lxmone-h3-workflow" {
		t.Fatalf("prefixed protocol = %s", prefixed.Protocol)
	}

	var unchanged model.ChannelModel
	if err := db.First(&unchanged, "id = ?", "h3-max").Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Protocol != "lxmone-h3-max-videos" || unchanged.CapabilityConfigJSON != legacy || unchanged.CapabilityVersion != 2 {
		t.Fatalf("H3 Max changed: protocol=%s version=%d config=%s", unchanged.Protocol, unchanged.CapabilityVersion, unchanged.CapabilityConfigJSON)
	}

	var workflow model.ChannelModel
	if err := db.First(&workflow, "id = ?", "h3-a").Error; err != nil {
		t.Fatal(err)
	}
	if workflow.Protocol != "lxmone-h3-workflow" || workflow.CapabilityConfigJSON != legacy || workflow.CapabilityVersion != 4 {
		t.Fatalf("existing workflow model changed: protocol=%s version=%d", workflow.Protocol, workflow.CapabilityVersion)
	}
}

func assertH3CCapabilityJSON(t *testing.T, raw string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	video, _ := root["video"].(map[string]any)
	resolutions, _ := video["resolutions"].([]any)
	if len(resolutions) != 2 || resolutions[0] != "480p" || resolutions[1] != "720p" || video["defaultResolution"] != "720p" {
		t.Fatalf("resolutions = %#v default=%#v", resolutions, video["defaultResolution"])
	}
	duration, _ := video["duration"].(map[string]any)
	if duration["min"] != float64(1) || duration["max"] != float64(15) || duration["default"] != float64(6) {
		t.Fatalf("duration = %#v", duration)
	}
	references, _ := video["references"].(map[string]any)
	if references["maxImages"] != float64(2) || references["maxVideos"] != float64(0) || references["maxAudios"] != float64(0) {
		t.Fatalf("references = %#v", references)
	}
}

func TestMigrateLXMoneH3CProtocolIgnoresCorruptConfig(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:migration-lxmone-h3c-corrupt?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ChannelModel{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	row := model.ChannelModel{ID: "broken", ModelKey: "minimax-h3-c", ProviderModelKey: "minimax-h3-c", Capability: "video", Protocol: "lxmone-h3-max-videos", CapabilityConfigJSON: "{", CapabilityVersion: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateLXMoneH3CProtocol(db); err != nil {
		t.Fatal(err)
	}
	var stored model.ChannelModel
	if err := db.First(&stored, "id = ?", "broken").Error; err != nil {
		t.Fatal(err)
	}
	if stored.Protocol != "lxmone-h3-max-videos" || stored.CapabilityConfigJSON != "{" || stored.CapabilityVersion != 1 {
		t.Fatalf("corrupt row changed: %#v", stored)
	}
}
