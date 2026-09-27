package handler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"infinite-canvas/backend/internal/service"
)

// 分片上传会话：把“导入本地媒体”拆成 开始→逐片→合并 三段，单片上限 8MB，
// 文件整体不再受 multipart 单请求大小限制（对齐 Concat 桌面端“任意大小直接入库”的体验）。
// 会话状态只存在内存（重启即失效 → 前端整传重试），磁盘暂存在系统临时目录，随会话清理。
const (
	chunkUploadChunkSize       = 8 << 20
	chunkUploadSlackBytes      = 64 << 10 // MaxBytesReader 允许的超片余量
	chunkUploadTTL             = 90 * time.Minute
	chunkUploadJanitorInterval = 5 * time.Minute
	chunkUploadMaxPerUser      = 32
	chunkUploadBodyCapJSON     = 16 << 10
)

type chunkUploadSessionState uint8

const (
	chunkUploadSessionActive chunkUploadSessionState = iota
	chunkUploadSessionCompleting
	chunkUploadSessionCancelled
	chunkUploadSessionCompleted
	chunkUploadSessionExpired
)

type chunkedUploadSession struct {
	ID             string
	UserID         string
	FileName       string
	Kind           string
	Size           int64
	Width          int
	Height         int
	DurationMs     int64
	IdempotencyKey string
	ChunkCount     int
	Dir            string
	CreatedAt      time.Time

	mu             sync.Mutex
	commitMu       sync.Mutex
	state          chunkUploadSessionState
	activeOps      int
	activePuts     int
	cleanupPending bool
	cleanupStarted bool
}

var chunkUploadSessions = struct {
	sync.Mutex
	m map[string]*chunkedUploadSession
}{m: make(map[string]*chunkedUploadSession)}

var chunkUploadJanitorOnce sync.Once

func newUploadSessionID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func (s *chunkedUploadSession) chunkPath(index int) string {
	return filepath.Join(s.Dir, fmt.Sprintf("chunk-%d", index))
}

func (s *chunkedUploadSession) hasAllChunks() bool {
	for i := 0; i < s.ChunkCount; i++ {
		info, err := os.Stat(s.chunkPath(i))
		if err != nil || info.Size() != s.chunkSizeAt(i) {
			return false
		}
	}
	return true
}

// chunkSizeAt 返回第 index 片的期望字节数（末片按文件余量，其余固定 chunkSize）。
func (s *chunkedUploadSession) chunkSizeAt(index int) int64 {
	if index == s.ChunkCount-1 {
		rest := s.Size - int64(index)*chunkUploadChunkSize
		if rest < 0 {
			return 0
		}
		return rest
	}
	return chunkUploadChunkSize
}

func startChunkUploadJanitor() {
	chunkUploadJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(chunkUploadJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				removeExpiredChunkSessions()
			}
		}()
	})
}

func lookupChunkSession(id string) *chunkedUploadSession {
	chunkUploadSessions.Lock()
	session := chunkUploadSessions.m[id]
	chunkUploadSessions.Unlock()
	return session
}

func removeChunkSessionIndex(id string, session *chunkedUploadSession) {
	chunkUploadSessions.Lock()
	if chunkUploadSessions.m[id] == session {
		delete(chunkUploadSessions.m, id)
	}
	chunkUploadSessions.Unlock()
}

// cleanupChunkSessionDir 只在没有活动文件操作时执行，且实际磁盘 IO 始终位于锁外。
func cleanupChunkSessionDir(session *chunkedUploadSession) {
	session.mu.Lock()
	if !session.cleanupPending || session.cleanupStarted || session.activeOps != 0 {
		session.mu.Unlock()
		return
	}
	session.cleanupStarted = true
	dir := session.Dir
	session.mu.Unlock()

	err := os.RemoveAll(dir)
	if err == nil {
		return
	}

	session.mu.Lock()
	// A failed cleanup must remain retryable. The session is already terminal
	// and no new operation can acquire it, so only reset the in-flight marker.
	session.cleanupStarted = false
	session.mu.Unlock()
	log.Printf("chunk upload session cleanup failed: upload_id=%s error=%v", session.ID, err)
}

func retireChunkSession(id string, session *chunkedUploadSession, state chunkUploadSessionState) {
	session.mu.Lock()
	session.state = state
	session.cleanupPending = true
	session.mu.Unlock()

	removeChunkSessionIndex(id, session)
	cleanupChunkSessionDir(session)
}

func releaseChunkSessionOperation(session *chunkedUploadSession, put bool) {
	session.mu.Lock()
	if put && session.activePuts > 0 {
		session.activePuts--
	}
	if session.activeOps > 0 {
		session.activeOps--
	}
	shouldCleanup := session.cleanupPending && session.activeOps == 0
	session.mu.Unlock()

	if shouldCleanup {
		cleanupChunkSessionDir(session)
	}
}

func reopenChunkSession(session *chunkedUploadSession) {
	session.mu.Lock()
	if session.state == chunkUploadSessionCompleting {
		session.state = chunkUploadSessionActive
	}
	session.mu.Unlock()

	releaseChunkSessionOperation(session, false)
}

// expireChunkSessionLocked is the authoritative TTL transition. Callers must
// hold session.mu and perform index removal/IO after releasing it.
func expireChunkSessionLocked(session *chunkedUploadSession, now time.Time) bool {
	if session.state != chunkUploadSessionActive || now.Sub(session.CreatedAt) <= chunkUploadTTL {
		return false
	}
	session.state = chunkUploadSessionExpired
	session.cleanupPending = true
	return true
}

func finishExpiredChunkSession(id string, session *chunkedUploadSession) {
	removeChunkSessionIndex(id, session)
	cleanupChunkSessionDir(session)
}

func removeExpiredChunkSessions() {
	now := time.Now()
	chunkUploadSessions.Lock()
	type candidate struct {
		id      string
		session *chunkedUploadSession
	}
	candidates := make([]candidate, 0, len(chunkUploadSessions.m))
	for id, sess := range chunkUploadSessions.m {
		candidates = append(candidates, candidate{id: id, session: sess})
	}
	chunkUploadSessions.Unlock()

	for _, item := range candidates {
		session := item.session
		session.mu.Lock()
		expired := expireChunkSessionLocked(session, now)
		session.mu.Unlock()
		if expired {
			finishExpiredChunkSession(item.id, session)
		}
	}
}

func beginChunkPut(id string, userID string) (*chunkedUploadSession, bool) {
	removeExpiredChunkSessions()
	session := lookupChunkSession(id)
	if session == nil {
		return nil, false
	}

	expired := false
	session.mu.Lock()
	if expireChunkSessionLocked(session, time.Now()) {
		expired = true
	} else if session.UserID != userID || session.state != chunkUploadSessionActive {
		session.mu.Unlock()
		return nil, false
	} else {
		session.activeOps++
		session.activePuts++
	}
	session.mu.Unlock()
	if expired {
		finishExpiredChunkSession(id, session)
		return nil, false
	}
	return session, true
}

func chunkSessionAcceptsWrites(session *chunkedUploadSession) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.state == chunkUploadSessionActive
}

// replaceChunkFile publishes a fully written temporary chunk. On Unix, Rename
// atomically replaces an existing chunk. Windows' MoveFile does not replace an
// existing path, so move the old complete chunk aside while the caller's
// session operation keeps complete/cleanup from observing the short gap.
func replaceChunkFile(tempPath string, finalPath string) error {
	renameErr := os.Rename(tempPath, finalPath)
	if renameErr == nil {
		return nil
	}
	if !os.IsExist(renameErr) {
		return renameErr
	}
	targetInfo, statErr := os.Lstat(finalPath)
	if statErr != nil {
		return renameErr
	}
	if !targetInfo.Mode().IsRegular() {
		return fmt.Errorf("chunk target is not a regular file: %w", renameErr)
	}

	backup, err := os.CreateTemp(filepath.Dir(finalPath), ".chunk-replace-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(finalPath, backupPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		restoreErr := os.Rename(backupPath, finalPath)
		if restoreErr != nil {
			// A failed MoveFile may leave a target behind. The old chunk is
			// authoritative, so remove that target before one restore retry.
			_ = os.Remove(finalPath)
			restoreErr = os.Rename(backupPath, finalPath)
		}
		if restoreErr != nil {
			return fmt.Errorf("%w (also failed to restore previous chunk: %v)", err, restoreErr)
		}
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		log.Printf("chunk upload backup cleanup failed: target=%s error=%v", finalPath, err)
	}
	return nil
}

// publishChunkFile serializes the short publication step, checks the session
// state both before and after Rename, and never holds a lock during chunk IO.
func publishChunkFile(session *chunkedUploadSession, index int, tempPath string) (bool, error) {
	session.commitMu.Lock()
	defer session.commitMu.Unlock()

	if !chunkSessionAcceptsWrites(session) {
		return false, nil
	}
	finalPath := session.chunkPath(index)
	if err := replaceChunkFile(tempPath, finalPath); err != nil {
		return false, err
	}
	if !chunkSessionAcceptsWrites(session) {
		// No later PUT can publish after cancellation/expiry. Remove the
		// just-published file before releasing the active operation.
		_ = os.Remove(finalPath)
		return false, nil
	}
	return true, nil
}

type chunkCompleteBeginResult uint8

const (
	chunkCompleteBeginOK chunkCompleteBeginResult = iota
	chunkCompleteBeginNotFound
	chunkCompleteBeginBusy
)

func beginChunkComplete(id string, userID string) (*chunkedUploadSession, chunkCompleteBeginResult) {
	removeExpiredChunkSessions()
	session := lookupChunkSession(id)
	if session == nil {
		return nil, chunkCompleteBeginNotFound
	}

	expired := false
	result := chunkCompleteBeginNotFound
	session.mu.Lock()
	if expireChunkSessionLocked(session, time.Now()) {
		expired = true
	} else if session.UserID == userID {
		switch session.state {
		case chunkUploadSessionActive:
			if session.activePuts != 0 {
				result = chunkCompleteBeginBusy
				break
			}
			session.state = chunkUploadSessionCompleting
			session.activeOps++
			result = chunkCompleteBeginOK
		case chunkUploadSessionCompleting:
			result = chunkCompleteBeginBusy
		}
	}
	session.mu.Unlock()
	if expired {
		finishExpiredChunkSession(id, session)
		return nil, chunkCompleteBeginNotFound
	}
	if result != chunkCompleteBeginOK {
		return nil, result
	}
	return session, result
}

type chunkCancelResult uint8

const (
	chunkCancelNotFound chunkCancelResult = iota
	chunkCancelAccepted
	chunkCancelCompletionOwns
)

func cancelChunkSession(id string, userID string) chunkCancelResult {
	removeExpiredChunkSessions()
	session := lookupChunkSession(id)
	if session == nil {
		return chunkCancelNotFound
	}

	expired := false
	session.mu.Lock()
	if expireChunkSessionLocked(session, time.Now()) {
		expired = true
	} else if session.UserID != userID {
		session.mu.Unlock()
		return chunkCancelNotFound
	} else {
		switch session.state {
		case chunkUploadSessionActive:
			session.state = chunkUploadSessionCancelled
			session.cleanupPending = true
			session.mu.Unlock()
			removeChunkSessionIndex(id, session)
			cleanupChunkSessionDir(session)
			return chunkCancelAccepted
		case chunkUploadSessionCompleting, chunkUploadSessionCompleted:
			// complete 已经取得提交权时优先完成；取消成为幂等 no-op，绝不删除其目录或已提交资源。
			session.mu.Unlock()
			return chunkCancelCompletionOwns
		default:
			session.mu.Unlock()
			return chunkCancelNotFound
		}
	}
	session.mu.Unlock()
	if expired {
		finishExpiredChunkSession(id, session)
	}
	return chunkCancelNotFound
}

func resolveChunkUploadIdempotencyKey(bodyKey string, headerKey string) (string, error) {
	bodyKey = strings.TrimSpace(bodyKey)
	headerKey = strings.TrimSpace(headerKey)
	if bodyKey != "" && headerKey != "" && bodyKey != headerKey {
		return "", fmt.Errorf("请求体与 X-Idempotency-Key 不一致")
	}
	if headerKey != "" {
		return headerKey, nil
	}
	return bodyKey, nil
}

// RegisterChunkedUploadRoutes 注册本地媒体分片上传四条接口（POST 开始 / PUT 上传片 / POST 合并 / DELETE 取消）。
func RegisterChunkedUploadRoutes(r *gin.RouterGroup, svc *service.Service) {
	startChunkUploadJanitor()

	r.POST("/resources/uploads", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		removeExpiredChunkSessions()
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "resources-upload:"+user.ID, policy.Request.ResourceUploadPerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, chunkUploadBodyCapJSON)
		var req struct {
			FileName       string `json:"fileName"`
			Kind           string `json:"kind"`
			Size           int64  `json:"size"`
			Width          int    `json:"width"`
			Height         int    `json:"height"`
			DurationMs     int64  `json:"durationMs"`
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		idempotencyKey, keyErr := resolveChunkUploadIdempotencyKey(req.IdempotencyKey, c.GetHeader("X-Idempotency-Key"))
		if keyErr != nil {
			fail(c, http.StatusConflict, keyErr)
			return
		}
		req.IdempotencyKey = idempotencyKey
		if req.FileName == "" || len(req.FileName) > 255 {
			fail(c, http.StatusBadRequest, fmt.Errorf("文件名不能为空且不能超过 255 个字符"))
			return
		}
		if req.Size <= 0 {
			fail(c, http.StatusBadRequest, fmt.Errorf("文件大小必须大于 0"))
			return
		}
		// 超账号存储总量的文件无论如何都会失败，提前给出明确提示。
		if policy.Resource.StoredFileGB > 0 && req.Size > int64(policy.Resource.StoredFileGB)<<30 {
			fail(c, http.StatusBadRequest, fmt.Errorf("文件超过账号存储总量上限 %dGB", policy.Resource.StoredFileGB))
			return
		}
		// 同一用户并发会话数兜底，防内存占用失控。
		active := 0
		chunkUploadSessions.Lock()
		for _, sess := range chunkUploadSessions.m {
			if sess.UserID == user.ID {
				active++
			}
		}
		chunkUploadSessions.Unlock()
		if active >= chunkUploadMaxPerUser {
			fail(c, http.StatusTooManyRequests, fmt.Errorf("同时进行中的上传过多，请稍后重试"))
			return
		}
		dir, err := os.MkdirTemp("", "canvas-chunk-upload-*")
		if err != nil {
			failService(c, err)
			return
		}
		session := &chunkedUploadSession{
			ID:             newUploadSessionID(),
			UserID:         user.ID,
			FileName:       req.FileName,
			Kind:           req.Kind,
			Size:           req.Size,
			Width:          req.Width,
			Height:         req.Height,
			DurationMs:     req.DurationMs,
			IdempotencyKey: req.IdempotencyKey,
			ChunkCount:     int((req.Size + chunkUploadChunkSize - 1) / chunkUploadChunkSize),
			Dir:            dir,
			CreatedAt:      time.Now(),
		}
		chunkUploadSessions.Lock()
		chunkUploadSessions.m[session.ID] = session
		chunkUploadSessions.Unlock()
		ok(c, gin.H{"uploadId": session.ID, "chunkSize": chunkUploadChunkSize, "chunkCount": session.ChunkCount})
	})

	r.PUT("/resources/uploads/:id/chunks/:index", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		session, found := beginChunkPut(c.Param("id"), user.ID)
		if !found {
			fail(c, http.StatusNotFound, fmt.Errorf("上传会话不存在或已过期，请重新导入"))
			return
		}
		defer releaseChunkSessionOperation(session, true)
		index, err := strconv.Atoi(c.Param("index"))
		if err != nil || index < 0 || index >= session.ChunkCount {
			fail(c, http.StatusBadRequest, fmt.Errorf("非法的分片序号"))
			return
		}
		expected := session.chunkSizeAt(index)
		if expected <= 0 {
			fail(c, http.StatusBadRequest, fmt.Errorf("非法的分片序号"))
			return
		}
		if !chunkSessionAcceptsWrites(session) {
			fail(c, http.StatusNotFound, fmt.Errorf("上传会话不存在或已过期，请重新导入"))
			return
		}
		// 单片限长（期望长度 + 少量余量），超长直接中断，避免内存/磁盘被恶意占用。
		body := http.MaxBytesReader(c.Writer, c.Request.Body, expected+chunkUploadSlackBytes)
		dst, err := os.CreateTemp(session.Dir, fmt.Sprintf(".chunk-%d-*.tmp", index))
		if err != nil {
			failService(c, err)
			return
		}
		tempPath := dst.Name()
		keepTemp := true
		defer func() {
			if keepTemp {
				_ = os.Remove(tempPath)
			}
		}()
		written, copyErr := io.CopyN(dst, body, expected)
		syncErr := dst.Sync()
		closeErr := dst.Close()
		var probe [1]byte
		extra, readErr := body.Read(probe[:])
		if copyErr != nil {
			fail(c, http.StatusBadRequest, fmt.Errorf("分片 %d 上传不完整，请重试", index))
			return
		}
		if syncErr != nil {
			failService(c, syncErr)
			return
		}
		if closeErr != nil {
			failService(c, closeErr)
			return
		}
		if readErr == nil || extra > 0 {
			fail(c, http.StatusBadRequest, fmt.Errorf("分片 %d 超过大小限制", index))
			return
		}
		published, publishErr := publishChunkFile(session, index, tempPath)
		if publishErr != nil {
			failService(c, publishErr)
			return
		}
		if !published {
			fail(c, http.StatusNotFound, fmt.Errorf("上传会话不存在或已过期，请重新导入"))
			return
		}
		keepTemp = false
		_ = written
		ok(c, gin.H{"index": index})
	})

	r.POST("/resources/uploads/:id/complete", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		id := c.Param("id")
		session, beginResult := beginChunkComplete(id, user.ID)
		switch beginResult {
		case chunkCompleteBeginNotFound:
			fail(c, http.StatusNotFound, fmt.Errorf("上传会话不存在或已过期，请重新导入"))
			return
		case chunkCompleteBeginBusy:
			fail(c, http.StatusConflict, fmt.Errorf("上传会话仍在写入或合并中，请稍后重试"))
			return
		}
		terminal := false
		defer func() {
			if terminal {
				retireChunkSession(id, session, chunkUploadSessionCompleted)
				releaseChunkSessionOperation(session, false)
				return
			}
			reopenChunkSession(session)
		}()
		if !session.hasAllChunks() {
			fail(c, http.StatusBadRequest, fmt.Errorf("上传文件不完整，请重新导入"))
			return
		}
		mergedPath := filepath.Join(session.Dir, "merged")
		merged, err := os.OpenFile(mergedPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			failService(c, err)
			return
		}
		total := int64(0)
		for i := 0; i < session.ChunkCount; i++ {
			part, openErr := os.Open(session.chunkPath(i))
			if openErr != nil {
				_ = merged.Close()
				failService(c, openErr)
				return
			}
			n, copyErr := io.Copy(merged, part)
			_ = part.Close()
			if copyErr != nil {
				_ = merged.Close()
				failService(c, copyErr)
				return
			}
			total += n
		}
		if closeErr := merged.Close(); closeErr != nil {
			failService(c, closeErr)
			return
		}
		if total != session.Size {
			terminal = true
			fail(c, http.StatusBadRequest, fmt.Errorf("上传文件不完整，请重新导入"))
			return
		}
		fh, err := os.Open(mergedPath)
		if err != nil {
			failService(c, err)
			return
		}
		defer fh.Close()
		// 一旦把完整文件交给资源服务，后续失败也结束本次会话，避免重复提交同一临时文件。
		terminal = true
		resource, svcErr := svc.UploadResourceFile(user.ID, session.FileName, session.Size, session.Kind, session.Width, session.Height, session.DurationMs, fh, session.IdempotencyKey)
		if svcErr != nil {
			failService(c, svcErr)
			return
		}
		ok(c, gin.H{"resource": resource})
	})

	r.DELETE("/resources/uploads/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		switch cancelChunkSession(c.Param("id"), user.ID) {
		case chunkCancelAccepted:
			ok(c, gin.H{"cancelled": true})
		case chunkCancelCompletionOwns:
			ok(c, gin.H{"cancelled": false})
		default:
			// 不存在、过期、已完成和他人会话统一 404，避免泄露 uploadId 归属。
			fail(c, http.StatusNotFound, fmt.Errorf("上传会话不存在或已过期"))
		}
	})
}
