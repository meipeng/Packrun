package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// uploadTempMarker 标记尚未提交的临时文件；启动清扫与运维排查都依赖这个标记。
const uploadTempMarker = ".incoming-"

// uploadMaxOverhead 估算 multipart 请求体里除文件内容之外的固定开销（边界串、字段头、文件名）。
// 仅用于提前拦截明显超限的请求，真实体积以实际写入字节数为准。
const uploadMaxOverhead = 4 << 10

// uploadFailure 描述写盘阶段可以直接回传给前端的失败。
// Cause 保存底层原因，只进日志不进响应，避免把服务器路径等细节暴露给客户端。
type uploadFailure struct {
	Status int
	Code   int
	Msg    string
	Cause  error
}

func (e *uploadFailure) Error() string { return e.Msg }

func (e *uploadFailure) Unwrap() error { return e.Cause }

func newUploadFailure(status, code int, msg string, cause error) *uploadFailure {
	return &uploadFailure{Status: status, Code: code, Msg: msg, Cause: cause}
}

// savePackageFile 把 src 流式写入 dst：先落到同目录临时文件，fsync 落盘后再原子改名。
// 进程崩溃、客户端断连或磁盘写满时都不会在仓库里留下半截包文件，
// 也不会出现「文件已存在但数据库没有记录」的孤儿包。
func savePackageFile(dst string, src io.Reader, maxBytes int64) (int64, string, error) {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, "", newUploadFailure(http.StatusInternalServerError, CodeInternalError, "创建存储目录失败", err)
	}

	tmp, out, err := createUploadTemp(dst)
	if err != nil {
		return 0, "", newUploadFailure(http.StatusInternalServerError, CodeInternalError, "写入存储失败", err)
	}
	// 未走到 committed 就返回时一律清掉半截临时文件。
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = out.Close() // 已关闭时返回错误，忽略即可
		_ = os.Remove(tmp)
	}()

	h := sha256.New()
	// 多读 1 字节用于判断是否越界，超限时立即中止，不必把整个包写完。
	written, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, maxBytes+1))
	if err != nil {
		return 0, "", classifyUploadWriteError(err)
	}
	if written > maxBytes {
		return 0, "", newUploadFailure(http.StatusRequestEntityTooLarge, CodeBadRequest,
			fmt.Sprintf("包大小超过上限 %d MB", maxBytes>>20), nil)
	}
	if err := out.Sync(); err != nil {
		return 0, "", newUploadFailure(http.StatusInternalServerError, CodeInternalError, "写入存储失败", err)
	}
	if err := out.Close(); err != nil {
		return 0, "", newUploadFailure(http.StatusInternalServerError, CodeInternalError, "写入存储失败", err)
	}
	// 关闭句柄后再改名，确保切换瞬间没有进程持有旧路径。
	if err := os.Rename(tmp, dst); err != nil {
		return 0, "", newUploadFailure(http.StatusInternalServerError, CodeInternalError, "写入存储失败", err)
	}
	committed = true
	syncDir(dir) // 尽力而为：让改名本身也落盘
	return written, hex.EncodeToString(h.Sum(nil)), nil
}

// createUploadTemp 在目标目录内创建唯一临时文件，保证随后的 rename 不跨文件系统。
func createUploadTemp(dst string) (string, *os.File, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		tmp := dst + uploadTempMarker + randomHex(4)
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return tmp, f, nil
		}
		lastErr = err
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("无法创建上传临时文件")
	}
	return "", nil, lastErr
}

// syncDir 把目录项变更刷到磁盘；多数文件系统不支持对目录 fsync，失败可忽略。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// classifyUploadWriteError 区分客户端断连、磁盘写满与其它写盘失败，给出可读提示。
func classifyUploadWriteError(err error) *uploadFailure {
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return newUploadFailure(http.StatusInsufficientStorage, CodeInternalError, "服务器磁盘空间不足，上传失败", err)
	case isClientDisconnect(err):
		return newUploadFailure(http.StatusBadRequest, CodeBadRequest, "上传已中断，请检查网络后重试", err)
	default:
		return newUploadFailure(http.StatusInternalServerError, CodeInternalError, "保存包文件失败", err)
	}
}

// isClientDisconnect 判断错误是否来自对端断连/取消，而不是本机故障。
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"broken pipe", "connection reset", "unexpected EOF", "client disconnected"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// uploadMaxFieldBytes 允许出现在文件段之前的普通表单字段上限。
const uploadMaxFieldBytes = 64 << 10

// nextFilePart 取到名为 file 的文件段。
// 只跳过小体积的普通字段：multipart.Part.Close 和 NextPart 都会把当前段剩余字节读完，
// 若允许任意大小的前置字段，就可能被拖进与本次上传无关的海量读取。
// 调用方在提前返回时不要 Close 文件段，交给 HTTP 服务端按 256KB 上限排空后关连接。
func nextFilePart(r *multipart.Reader) (*multipart.Part, error) {
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			if isClientDisconnect(err) {
				return nil, classifyUploadWriteError(err)
			}
			return nil, newUploadFailure(http.StatusBadRequest, CodeBadRequest, "上传请求格式不正确，请重新选择文件", err)
		}
		if part.FormName() == "file" {
			return part, nil
		}
		n, copyErr := io.Copy(io.Discard, io.LimitReader(part, uploadMaxFieldBytes+1))
		if copyErr != nil {
			if isClientDisconnect(copyErr) {
				return nil, classifyUploadWriteError(copyErr)
			}
			return nil, newUploadFailure(http.StatusBadRequest, CodeBadRequest, "上传请求格式不正确，请重新选择文件", copyErr)
		}
		if n > uploadMaxFieldBytes {
			return nil, newUploadFailure(http.StatusBadRequest, CodeBadRequest,
				"上传请求格式不正确，请重新选择文件", errors.New("普通表单字段过大"))
		}
		_ = part.Close() // 已读到段尾，这里只是收尾
	}
}

// uploadPayloadEstimate 由请求体长度估算文件字节数：扣掉 multipart 固定开销。
func uploadPayloadEstimate(contentLength int64) int64 {
	if contentLength <= uploadMaxOverhead {
		return 0
	}
	return contentLength - uploadMaxOverhead
}

// writeUploadFailure 把写盘阶段的失败转成响应，并把底层原因写进日志便于排查。
func writeUploadFailure(c *gin.Context, err error) {
	var uf *uploadFailure
	if errors.As(err, &uf) {
		if uf.Cause != nil {
			log.Printf("[packages] 上传失败 user=%s: %s: %v", currentUser(c).Username, uf.Msg, uf.Cause)
		}
		fail(c, uf.Status, uf.Code, uf.Msg)
		return
	}
	log.Printf("[packages] 上传失败 user=%s: %v", currentUser(c).Username, err)
	internal(c, "上传失败")
}

// sweepStaleUploads 清理进程异常退出时残留的临时文件。
// 只删除超过 minAge 的临时文件，避免误删正在进行中的上传。
func sweepStaleUploads(root string, minAge time.Duration) int {
	removed := 0
	now := time.Now()
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if !strings.Contains(entry.Name(), uploadTempMarker) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < minAge {
			return nil
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	return removed
}
