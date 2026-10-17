package canvas

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"yingce/backend/internal/kernel"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCanvasOperationsHistoryCapturesPreimageAndReplaysWithoutDuplicates(t *testing.T) {
	svc := newCanvasOperationTestService(t)
	created, err := svc.ApplyUserCanvasOperations("user", "canvas", CanvasOperationsRequest{
		MutationID: "create", Operations: []CanvasOperation{
			{Type: "create_canvas", Patch: map[string]json.RawMessage{"title": json.RawMessage(`"original"`)}},
			{Type: "create_node", Node: json.RawMessage(`{"id":"node","metadata":{"title":"old"}}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := svc.CanvasHistory("user", "canvas")
	if err != nil || len(history.Snapshots) != 0 {
		t.Fatalf("creation should not capture an empty preimage: %+v, %v", history, err)
	}
	request := CanvasOperationsRequest{MutationID: "edit", Operations: []CanvasOperation{{
		Type: "patch_node", ID: "node", Patch: map[string]json.RawMessage{"metadata.title": json.RawMessage(`"new"`)},
		Before: map[string]json.RawMessage{"metadata.title": json.RawMessage(`"old"`)},
	}}}
	saved, err := svc.ApplyUserCanvasOperations("user", "canvas", request)
	if err != nil {
		t.Fatal(err)
	}
	history, err = svc.CanvasHistory("user", "canvas")
	if err != nil || history.CurrentRevision != saved.Revision || len(history.Snapshots) != 1 {
		t.Fatalf("operations history = %+v, %v", history, err)
	}
	snapshot, err := svc.CanvasHistorySnapshot("user", "canvas", history.Snapshots[0].ID)
	if err != nil || snapshot.Revision != created.Revision || snapshot.Reason != "automatic" || !strings.Contains(snapshot.PayloadJSON, `"title":"old"`) {
		t.Fatalf("preimage = %+v, %v", snapshot, err)
	}
	replayed, err := svc.ApplyUserCanvasOperations("user", "canvas", request)
	if err != nil || replayed.Revision != saved.Revision {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	restored, err := svc.RestoreCanvasHistory("user", "canvas", snapshot.ID, &saved.Revision)
	if err != nil || restored.Revision != saved.Revision+1 {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	history, err = svc.CanvasHistory("user", "canvas")
	if err != nil || len(history.Snapshots) != 2 || history.Snapshots[0].Reason != "before_restore" {
		t.Fatalf("replay/restore history = %+v, %v", history, err)
	}

	// Retrying an acknowledged mutation after a restore returns its old receipt;
	// it must not reapply the edit or capture another snapshot.
	replayed, err = svc.ApplyUserCanvasOperations("user", "canvas", request)
	if err != nil || replayed.Revision != saved.Revision {
		t.Fatalf("post-restore replay = %+v, %v", replayed, err)
	}
	document, err := svc.UserCanvasProject("user", "canvas")
	if err != nil || !strings.Contains(string(document), `"title":"old"`) {
		t.Fatalf("replay changed restored content: %s, %v", document, err)
	}
	history, err = svc.CanvasHistory("user", "canvas")
	if err != nil || len(history.Snapshots) != 2 || history.CurrentRevision != restored.Revision {
		t.Fatalf("replay changed restored history: %+v, %v", history, err)
	}
}

func TestCanvasOperationsHistoryFailureRollsBackCanvasSnapshotAndReceipt(t *testing.T) {
	for _, stage := range []string{"snapshot", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			svc, db := newCanvasOperationsHistoryTestService(t)
			before := model.CanvasProject{ID: "canvas", UserID: "user", Title: "original", PayloadJSON: `{"nodes":[],"connections":[]}`}
			if err := svc.repo.UpsertCanvasProject(&before); err != nil {
				t.Fatal(err)
			}
			name := "test:canvas_history_failure"
			injected := errors.New("injected " + stage + " failure")
			callback := func(tx *gorm.DB) {
				if (stage == "snapshot" && tx.Statement.Table == "canvas_snapshots") || (stage == "receipt" && tx.Statement.Table == "canvas_mutation_receipts") {
					tx.AddError(injected)
				}
			}
			remove := func() error { return db.Callback().Create().Remove(name) }
			var registerErr error
			if stage == "snapshot" {
				registerErr = db.Callback().Create().Before("gorm:create").Register(name, callback)
			} else {
				registerErr = db.Callback().Update().Before("gorm:update").Register(name, callback)
				remove = func() error { return db.Callback().Update().Remove(name) }
			}
			if registerErr != nil {
				t.Fatal(registerErr)
			}
			t.Cleanup(func() { _ = remove() })
			request := CanvasOperationsRequest{MutationID: "edit", Operations: []CanvasOperation{{
				Type: "set_field", Field: "title", Value: json.RawMessage(`"edited"`), BeforeValue: json.RawMessage(`"original"`),
			}}}
			if _, err := svc.ApplyUserCanvasOperations("user", "canvas", request); !errors.Is(err, injected) {
				t.Fatalf("failure = %v, want injected error", err)
			}
			stored, err := svc.repo.CanvasProjectForUser("user", "canvas")
			if err != nil || stored.Revision != before.Revision || stored.Title != before.Title || stored.PayloadJSON != before.PayloadJSON {
				t.Fatalf("failed transaction changed content: %+v, %v", stored, err)
			}
			for _, table := range []any{&model.CanvasSnapshot{}, &model.CanvasSnapshotResource{}, &model.CanvasMutationReceipt{}} {
				var count int64
				if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("failed transaction left %T rows: %d, %v", table, count, err)
				}
			}
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			saved, err := svc.ApplyUserCanvasOperations("user", "canvas", request)
			if err != nil || saved.Revision != before.Revision+1 {
				t.Fatalf("same request could not retry after rollback: %+v, %v", saved, err)
			}
			history, err := svc.CanvasHistory("user", "canvas")
			if err != nil || len(history.Snapshots) != 1 {
				t.Fatalf("retry history = %+v, %v", history, err)
			}
		})
	}
}

func TestCanvasOperationsHistoryProtectsPreimageResources(t *testing.T) {
	for _, ready := range []bool{true, false} {
		name := "missing"
		if ready {
			name = "ready"
		}
		t.Run(name, func(t *testing.T) {
			svc, db := newCanvasOperationsHistoryTestService(t)
			if ready {
				if err := db.Create(&model.Resource{ID: "history-resource", UserID: "user", Status: model.ResourceStatusReady}).Error; err != nil {
					t.Fatal(err)
				}
			}
			node := json.RawMessage(`{"id":"node","metadata":{"resourceId":"history-resource"}}`)
			before := model.CanvasProject{ID: "canvas", UserID: "user", PayloadJSON: `{"nodes":[` + string(node) + `],"connections":[]}`}
			if err := svc.repo.UpsertCanvasProject(&before); err != nil {
				t.Fatal(err)
			}
			request := CanvasOperationsRequest{MutationID: "remove", Operations: []CanvasOperation{{Type: "remove_node", ID: "node", Expected: node}}}
			_, err := svc.ApplyUserCanvasOperations("user", "canvas", request)
			if !ready {
				var appError *kernel.AppError
				if !errors.As(err, &appError) || appError.Status != http.StatusConflict || appError.Code != kernel.CodeCanvasResourcesMissing {
					t.Fatalf("missing preimage resource = %v, want resource conflict", err)
				}
				stored, readErr := svc.repo.CanvasProjectForUser("user", "canvas")
				if readErr != nil || stored.Revision != before.Revision || stored.PayloadJSON != before.PayloadJSON {
					t.Fatalf("missing resource changed canvas: %+v, %v", stored, readErr)
				}
				for _, table := range []any{&model.CanvasSnapshot{}, &model.CanvasSnapshotResource{}, &model.CanvasMutationReceipt{}} {
					var count int64
					if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("missing resource left %T rows: %d, %v", table, count, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.repo.RequireNoCanvasHistoryReferences([]string{"history-resource"}); !errors.Is(err, repository.ErrCanvasHistoryResourceReferenced) {
				t.Fatalf("removed resource not protected by history: %v", err)
			}
			var refs []model.CanvasSnapshotResource
			if err := db.Find(&refs).Error; err != nil || len(refs) != 1 || refs[0].ResourceID != "history-resource" {
				t.Fatalf("history references = %+v, %v", refs, err)
			}
		})
	}
}

func newCanvasOperationsHistoryTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "history-operations.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CanvasEditLease{}, &model.CanvasProject{}, &model.CanvasSnapshot{}, &model.CanvasSnapshotResource{}, &model.CanvasMutationReceipt{}, &model.Resource{}, &model.Asset{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return New(repository.New(db), nil), db
}
