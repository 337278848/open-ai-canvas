package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"infinite-canvas/backend/internal/auth"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestChunkedUploadRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api")
	RegisterChunkedUploadRoutes(group, &service.Service{})
	wanted := map[string]bool{
		"POST /api/resources/uploads":                  false,
		"PUT /api/resources/uploads/:id/chunks/:index": false,
		"POST /api/resources/uploads/:id/complete":     false,
		"DELETE /api/resources/uploads/:id":            false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := wanted[key]; exists {
			wanted[key] = true
		}
	}
	for route, found := range wanted {
		if !found {
			t.Errorf("route %s is not registered", route)
		}
	}
}

func TestResolveChunkUploadIdempotencyKey(t *testing.T) {
	tests := []struct {
		name      string
		bodyKey   string
		headerKey string
		want      string
		wantErr   bool
	}{
		{name: "header only", headerKey: "header-key", want: "header-key"},
		{name: "body only", bodyKey: "body-key", want: "body-key"},
		{name: "same values", bodyKey: " same-key ", headerKey: "same-key", want: "same-key"},
		{name: "conflicting values", bodyKey: "body-key", headerKey: "header-key", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveChunkUploadIdempotencyKey(tt.bodyKey, tt.headerKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("key=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestChunkedUploadCreateRejectsConflictingIdempotencyKeys(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/resources/uploads", strings.NewReader(`{"fileName":"test.bin","kind":"file","size":1,"idempotencyKey":"body-key"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Idempotency-Key", "header-key")
	request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookies["owner"]})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflicting key status=%d body=%s", response.Code, response.Body.String())
	}
	chunkUploadSessions.Lock()
	sessionCount := len(chunkUploadSessions.m)
	chunkUploadSessions.Unlock()
	if sessionCount != 0 {
		t.Fatal("conflicting idempotency keys created an upload session")
	}
}

func TestChunkedUploadCreateUsesHeaderIdempotencyKey(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/resources/uploads", strings.NewReader(`{"fileName":"test.bin","kind":"file","size":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Idempotency-Key", "header-only-key")
	request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookies["owner"]})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("header-only key status=%d body=%s", response.Code, response.Body.String())
	}

	chunkUploadSessions.Lock()
	var session *chunkedUploadSession
	for _, candidate := range chunkUploadSessions.m {
		session = candidate
		break
	}
	chunkUploadSessions.Unlock()
	if session == nil || session.IdempotencyKey != "header-only-key" {
		t.Fatalf("stored idempotency key=%q", func() string {
			if session == nil {
				return "<nil session>"
			}
			return session.IdempotencyKey
		}())
	}
	if cancel := cancelChunkUploadRequest(router, session.ID, cookies["owner"]); cancel.Code != http.StatusOK {
		t.Fatalf("cleanup cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
}

func TestChunkedUploadPutPublishesOnlyAfterCompleteTemporaryWrite(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	session := addChunkUploadSessionForTest(t, "atomic-put", "owner")
	if err := os.WriteFile(session.chunkPath(0), []byte("o"), 0o600); err != nil {
		t.Fatal(err)
	}

	shortRequest := httptest.NewRequest(http.MethodPut, "/api/resources/uploads/"+session.ID+"/chunks/0", strings.NewReader(""))
	shortRequest.Header.Set("Content-Type", "application/octet-stream")
	shortRequest.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookies["owner"]})
	shortResponse := httptest.NewRecorder()
	router.ServeHTTP(shortResponse, shortRequest)
	if shortResponse.Code != http.StatusBadRequest {
		t.Fatalf("short PUT status=%d body=%s", shortResponse.Code, shortResponse.Body.String())
	}
	if got, err := os.ReadFile(session.chunkPath(0)); err != nil || string(got) != "o" {
		t.Fatalf("short PUT changed the previously published chunk: got=%q err=%v", got, err)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/resources/uploads/"+session.ID+"/chunks/0", strings.NewReader("n"))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookies["owner"]})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("successful PUT status=%d body=%s", response.Code, response.Body.String())
	}
	if got, err := os.ReadFile(session.chunkPath(0)); err != nil || string(got) != "n" {
		t.Fatalf("successful PUT did not publish the complete chunk: got=%q err=%v", got, err)
	}
	entries, err := os.ReadDir(session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".chunk-replace-") {
			t.Fatalf("replace backup was left behind: %s", entry.Name())
		}
	}
}

func TestReplaceChunkFileDoesNotMoveUnexpectedDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "chunk-0")
	if err := os.Mkdir(finalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	tempFile, err := os.CreateTemp(dir, ".chunk-0-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tempPath := tempFile.Name()
	if _, err := tempFile.WriteString("new"); err != nil {
		_ = tempFile.Close()
		t.Fatal(err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatal(err)
	}

	if err := replaceChunkFile(tempPath, finalPath); err == nil {
		t.Fatal("replaceChunkFile unexpectedly replaced a directory target")
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("unexpected directory target was moved or replaced")
	}
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("temporary chunk was lost after rejected replacement: %v", err)
	}
}

func TestChunkUploadCleanupFailureCanRetry(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	session := addChunkUploadSessionForTest(t, "cleanup-retry", "owner")
	invalidDir := "chunk-cleanup\x00failure"
	if err := os.RemoveAll(invalidDir); err == nil {
		t.Skip("platform accepted the invalid cleanup path")
	}
	session.mu.Lock()
	session.Dir = invalidDir
	session.state = chunkUploadSessionCancelled
	session.cleanupPending = true
	session.mu.Unlock()

	cleanupChunkSessionDir(session)
	session.mu.Lock()
	cleanupStarted := session.cleanupStarted
	session.mu.Unlock()
	if cleanupStarted {
		t.Fatal("failed cleanup permanently kept cleanupStarted=true")
	}

	validDir := t.TempDir()
	session.mu.Lock()
	session.Dir = validDir
	session.mu.Unlock()
	cleanupChunkSessionDir(session)
	if _, err := os.Stat(validDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup retry did not remove the directory: %v", err)
	}
}

func TestCancelChunkedUploadHTTP(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	session := addChunkUploadSessionForTest(t, "owned-upload", "owner")
	if err := os.WriteFile(session.chunkPath(0), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	wrongUser := cancelChunkUploadRequest(router, session.ID, cookies["other"])
	if wrongUser.Code != http.StatusNotFound {
		t.Fatalf("wrong user status=%d body=%s", wrongUser.Code, wrongUser.Body.String())
	}
	if lookupChunkSession(session.ID) != session {
		t.Fatal("wrong user removed the upload session")
	}
	if _, err := os.Stat(session.Dir); err != nil {
		t.Fatalf("wrong user removed the upload directory: %v", err)
	}

	owner := cancelChunkUploadRequest(router, session.ID, cookies["owner"])
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), `"cancelled":true`) {
		t.Fatalf("owner cancel status=%d body=%s", owner.Code, owner.Body.String())
	}
	if lookupChunkSession(session.ID) != nil {
		t.Fatal("cancelled session remains indexed")
	}
	if _, err := os.Stat(session.Dir); !os.IsNotExist(err) {
		t.Fatalf("cancelled upload directory still exists: %v", err)
	}

	repeated := cancelChunkUploadRequest(router, session.ID, cookies["owner"])
	if repeated.Code != http.StatusNotFound {
		t.Fatalf("repeated cancel status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	missing := cancelChunkUploadRequest(router, "missing-upload", cookies["owner"])
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing cancel status=%d body=%s", missing.Code, missing.Body.String())
	}
	unauthenticated := cancelChunkUploadRequest(router, "missing-upload", "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated cancel status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
}

func TestCancelChunkedUploadDefersCleanupUntilPutReleases(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	session := addChunkUploadSessionForTest(t, "put-race", "owner")
	acquired, found := beginChunkPut(session.ID, "owner")
	if !found || acquired != session {
		t.Fatal("failed to acquire PUT operation")
	}

	response := cancelChunkUploadRequest(router, session.ID, cookies["owner"])
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"cancelled":true`) {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
	}
	if lookupChunkSession(session.ID) != nil {
		t.Fatal("cancelled PUT session remains indexed")
	}
	if _, err := os.Stat(session.Dir); err != nil {
		t.Fatalf("directory was removed while PUT still held it: %v", err)
	}

	releaseChunkSessionOperation(session, true)
	if _, err := os.Stat(session.Dir); !os.IsNotExist(err) {
		t.Fatalf("directory was not removed after PUT released it: %v", err)
	}
}

func TestCancelChunkedUploadLetsCompletingSessionWin(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	session := addChunkUploadSessionForTest(t, "complete-race", "owner")
	acquired, result := beginChunkComplete(session.ID, "owner")
	if result != chunkCompleteBeginOK || acquired != session {
		t.Fatalf("begin complete result=%v session=%p", result, acquired)
	}

	response := cancelChunkUploadRequest(router, session.ID, cookies["owner"])
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"cancelled":false`) {
		t.Fatalf("cancel during complete status=%d body=%s", response.Code, response.Body.String())
	}
	if lookupChunkSession(session.ID) != session {
		t.Fatal("DELETE removed a completing session")
	}
	if _, err := os.Stat(session.Dir); err != nil {
		t.Fatalf("DELETE removed the directory used by complete: %v", err)
	}

	retireChunkSession(session.ID, session, chunkUploadSessionCompleted)
	if _, err := os.Stat(session.Dir); err != nil {
		t.Fatalf("directory was removed before complete released it: %v", err)
	}
	releaseChunkSessionOperation(session, false)
	if _, err := os.Stat(session.Dir); !os.IsNotExist(err) {
		t.Fatalf("directory was not removed after complete released it: %v", err)
	}
}

func TestIncompleteChunkedUploadCompleteKeepsSession(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	router, cookies := newChunkUploadTestRouter(t)
	session := addChunkUploadSessionForTest(t, "incomplete-upload", "owner")

	request := httptest.NewRequest(http.MethodPost, "/api/resources/uploads/"+session.ID+"/complete", nil)
	request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookies["owner"]})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("incomplete complete status=%d body=%s", response.Code, response.Body.String())
	}
	if lookupChunkSession(session.ID) != session {
		t.Fatal("incomplete complete removed the session")
	}
	if _, err := os.Stat(session.Dir); err != nil {
		t.Fatalf("incomplete complete removed the directory: %v", err)
	}
}

func TestExpiredChunkUploadCleanupWaitsForActiveOperation(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	session := addChunkUploadSessionForTest(t, "expired-race", "owner")
	session.mu.Lock()
	session.CreatedAt = time.Now().Add(-chunkUploadTTL - time.Minute)
	session.mu.Unlock()
	acquired, found := beginChunkPut(session.ID, "owner")
	if found || acquired != nil {
		t.Fatal("expired session unexpectedly accepted a PUT")
	}
	if lookupChunkSession(session.ID) != nil {
		t.Fatal("expired session remains indexed")
	}
	if _, err := os.Stat(session.Dir); !os.IsNotExist(err) {
		t.Fatalf("expired session directory still exists: %v", err)
	}
}

func TestExpiredChunkUploadCompleteIsRejected(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	session := addChunkUploadSessionForTest(t, "expired-complete", "owner")
	session.mu.Lock()
	session.CreatedAt = time.Now().Add(-chunkUploadTTL - time.Minute)
	session.mu.Unlock()

	acquired, result := beginChunkComplete(session.ID, "owner")
	if acquired != nil || result != chunkCompleteBeginNotFound {
		t.Fatalf("expired complete result=%v session=%p", result, acquired)
	}
	if lookupChunkSession(session.ID) != nil {
		t.Fatal("expired complete session remains indexed")
	}
	if _, err := os.Stat(session.Dir); !os.IsNotExist(err) {
		t.Fatalf("expired complete directory still exists: %v", err)
	}
}

func TestChunkUploadSessionTransitionsAreLinearized(t *testing.T) {
	resetChunkUploadSessionsForTest(t)
	for i := 0; i < 32; i++ {
		session := addChunkUploadSessionForTest(t, "linearized-upload", "owner")
		acquired, found := beginChunkPut(session.ID, "owner")
		if !found || acquired != session {
			t.Fatalf("iteration %d: failed to acquire PUT operation", i)
		}
		session.mu.Lock()
		session.CreatedAt = time.Now().Add(-chunkUploadTTL - time.Minute)
		session.mu.Unlock()

		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			removeExpiredChunkSessions()
		}()
		go func() {
			defer wg.Done()
			_ = cancelChunkSession(session.ID, "owner")
		}()
		go func() {
			defer wg.Done()
			_, _ = beginChunkComplete(session.ID, "owner")
		}()
		wg.Wait()

		if _, err := os.Stat(session.Dir); err != nil {
			t.Fatalf("iteration %d: directory removed while PUT was active: %v", i, err)
		}
		releaseChunkSessionOperation(session, true)
		if _, err := os.Stat(session.Dir); !os.IsNotExist(err) {
			t.Fatalf("iteration %d: directory was not removed after PUT release: %v", i, err)
		}
		if lookupChunkSession(session.ID) != nil {
			t.Fatalf("iteration %d: terminal session remains indexed", i)
		}
	}
}

func newChunkUploadTestRouter(t *testing.T) (*gin.Engine, map[string]string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	for _, record := range []any{
		&model.User{ID: "owner", Username: "owner", Email: "owner@example.invalid", Role: model.UserRoleUser, Status: model.UserStatusActive},
		&model.User{ID: "other", Username: "other", Email: "other@example.invalid", Role: model.UserRoleUser, Status: model.UserStatusActive},
		&model.AuthSession{ID: "owner-session", UserID: "owner", TokenHash: auth.HashToken("owner-token"), ExpiresAt: time.Now().Add(time.Hour)},
		&model.AuthSession{ID: "other-session", UserID: "other", TokenHash: auth.HashToken("other-token"), ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if err := db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}

	svc := service.New(repository.New(db), t.TempDir())
	previousRuntimeService := runtimeService
	ConfigureRuntime(svc)
	t.Cleanup(func() { runtimeService = previousRuntimeService })
	router := gin.New()
	RegisterChunkedUploadRoutes(router.Group("/api"), svc)
	return router, map[string]string{
		"owner": "owner-session.owner-token",
		"other": "other-session.other-token",
	}
}

func addChunkUploadSessionForTest(t *testing.T, id string, userID string) *chunkedUploadSession {
	t.Helper()
	session := &chunkedUploadSession{
		ID:         id,
		UserID:     userID,
		FileName:   "test.bin",
		Size:       1,
		ChunkCount: 1,
		Dir:        t.TempDir(),
		CreatedAt:  time.Now(),
	}
	chunkUploadSessions.Lock()
	chunkUploadSessions.m[id] = session
	chunkUploadSessions.Unlock()
	return session
}

func cancelChunkUploadRequest(router *gin.Engine, uploadID string, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodDelete, "/api/resources/uploads/"+uploadID, nil)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookie})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func resetChunkUploadSessionsForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		chunkUploadSessions.Lock()
		sessions := make([]*chunkedUploadSession, 0, len(chunkUploadSessions.m))
		for _, session := range chunkUploadSessions.m {
			sessions = append(sessions, session)
		}
		chunkUploadSessions.m = make(map[string]*chunkedUploadSession)
		chunkUploadSessions.Unlock()
		for _, session := range sessions {
			session.mu.Lock()
			session.state = chunkUploadSessionCancelled
			session.cleanupPending = true
			session.activeOps = 0
			session.activePuts = 0
			session.mu.Unlock()
			cleanupChunkSessionDir(session)
		}
	}
	reset()
	t.Cleanup(reset)
}
