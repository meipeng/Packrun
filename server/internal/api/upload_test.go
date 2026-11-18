package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/packrun/packrun/internal/model"
)

// errReader 先吐出预设字节，再以给定错误中止，用于模拟客户端中途断连。
type errReader struct {
	data []byte
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

// uploadTempFiles 列出目录下所有未提交的上传临时文件。
func uploadTempFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if strings.Contains(entry.Name(), uploadTempMarker) {
			found = append(found, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("遍历目录失败: %v", err)
	}
	return found
}

// countPackageFiles 统计仓库目录内的普通文件数量。
func countPackageFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		count++
		return nil
	}); err != nil {
		t.Fatalf("遍历目录失败: %v", err)
	}
	return count
}

func TestSavePackageFileCommitsAtomically(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "200601", "abc.zip")
	payload := []byte("PK\x03\x04payload")

	written, sum, err := savePackageFile(dst, bytes.NewReader(payload), maxPackageSize)
	if err != nil {
		t.Fatalf("写入包文件失败: %v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("写入字节数 = %d，期望 %d", written, len(payload))
	}
	want := sha256.Sum256(payload)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("摘要 = %s，期望 %s", sum, hex.EncodeToString(want[:]))
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("落盘内容不符: %v", err)
	}
	if temps := uploadTempFiles(t, root); len(temps) != 0 {
		t.Fatalf("提交后不应残留临时文件: %v", temps)
	}
}

func TestSavePackageFileAbortsOnOversize(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "abc.zip")

	_, _, err := savePackageFile(dst, bytes.NewReader(make([]byte, 100)), 8)
	var uf *uploadFailure
	if !errors.As(err, &uf) || uf.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应返回 413，实际 %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("超限不应留下目标文件")
	}
	if temps := uploadTempFiles(t, root); len(temps) != 0 {
		t.Fatalf("超限后不应残留临时文件: %v", temps)
	}
}

func TestSavePackageFileCleansUpOnReadError(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "abc.zip")

	src := &errReader{data: []byte("PK\x03\x04partial"), err: errors.New("read tcp: connection reset by peer")}
	_, _, err := savePackageFile(dst, src, maxPackageSize)
	var uf *uploadFailure
	if !errors.As(err, &uf) || uf.Status != http.StatusBadRequest || !strings.Contains(uf.Msg, "上传已中断") {
		t.Fatalf("断连应给出可读提示，实际 %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("中断不应留下目标文件")
	}
	if temps := uploadTempFiles(t, root); len(temps) != 0 {
		t.Fatalf("中断后不应残留临时文件: %v", temps)
	}
}

func TestUploadInterruptedBodyLeavesNoPartialPackage(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	// 构造完整的 multipart 请求体后截断，并从断点处返回错误，模拟上传中途断网。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "app.zip")
	if err != nil {
		t.Fatalf("构造上传表单失败: %v", err)
	}
	if _, err := fw.Write(makeStoredZip(t, "big.bin", 512<<10)); err != nil {
		t.Fatalf("写入上传内容失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭上传表单失败: %v", err)
	}
	body := buf.Bytes()
	cut := len(body) * 2 / 3

	req := httptest.NewRequest(http.MethodPost, "/api/v1/packages",
		&errReader{data: body[:cut], err: errors.New("read tcp: connection reset by peer")})
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp apiResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusBadRequest || !strings.Contains(resp.Message, "上传已中断") {
		t.Fatalf("断连应 400 并提示重试，实际 %d / %q", w.Code, resp.Message)
	}
	if n := countPackageFiles(t, app.Cfg.Storage.PackageDir); n != 0 {
		t.Fatalf("中断后仓库应无文件，实际 %d 个", n)
	}

	var pkgCount int64
	if err := db.Model(&model.Package{}).Count(&pkgCount).Error; err != nil {
		t.Fatalf("统计包记录失败: %v", err)
	}
	if pkgCount != 0 {
		t.Fatalf("中断后不应登记包记录，实际 %d 条", pkgCount)
	}
}

func TestUploadRejectsOversizedContentLength(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	// 只声明超大长度，不真的构造 200MB 请求体：应在读包之前就拒绝。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/packages", strings.NewReader("PK"))
	req.ContentLength = int64(maxPackageSize) + uploadMaxOverhead + 1
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp apiResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(resp.Message, "超过上限") {
		t.Fatalf("声明超限应 413，实际 %d / %q", w.Code, resp.Message)
	}
}

func TestUploadRejectsMissingFileField(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("note", "no file here"); err != nil {
		t.Fatalf("写入表单字段失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/packages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp apiResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusBadRequest || resp.Message != "请选择要上传的包文件" {
		t.Fatalf("缺少文件字段应 400，实际 %d / %q", w.Code, resp.Message)
	}
}

// 超大的普通表单字段不应把服务端拖进无意义的读取。
func TestUploadRejectsOversizedFormField(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("note", strings.Repeat("x", uploadMaxFieldBytes+16)); err != nil {
		t.Fatalf("写入表单字段失败: %v", err)
	}
	fw, err := mw.CreateFormFile("file", "app.zip")
	if err != nil {
		t.Fatalf("构造文件段失败: %v", err)
	}
	if _, err := fw.Write(makeStoredZip(t, "a.txt", 8)); err != nil {
		t.Fatalf("写入文件段失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/packages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp apiResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusBadRequest || resp.Message != "上传请求格式不正确，请重新选择文件" {
		t.Fatalf("超大表单字段应 400，实际 %d / %q", w.Code, resp.Message)
	}
	if n := countPackageFiles(t, app.Cfg.Storage.PackageDir); n != 0 {
		t.Fatalf("被拒绝的请求不应写盘，实际 %d 个文件", n)
	}
}

// 判断超限时是否及时停读：读到 maxBytes+1 之后必须结束，不能把整个流读完。
func TestSavePackageFileStopsReadingAtLimit(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "abc.zip")

	src := &errReader{data: make([]byte, 4096), err: errors.New("读取超出预期：超限后仍在继续读")}
	_, _, err := savePackageFile(dst, src, 16)
	if err == nil {
		t.Fatalf("超限应返回错误")
	}
	var uf *uploadFailure
	if errors.As(err, &uf) && strings.Contains(uf.Msg, "读取超出预期") {
		t.Fatalf("超限后仍在继续读取上游数据流")
	}
	if len(src.data) == 0 {
		t.Fatalf("超限后仍把上游数据流读空了")
	}
}

func TestUploadRejectsUnsupportedExtension(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	rec, resp := doUpload(t, router, "app.tar.gz", makeStoredZip(t, "a.txt", 8), token)
	if rec.Code != http.StatusBadRequest || resp.Message != "仅支持 .zip 与 .jar 包" {
		t.Fatalf("不支持的扩展名应 400，实际 %d / %q", rec.Code, resp.Message)
	}
}

func TestUploadRejectsEmptyFile(t *testing.T) {
	app, router, db := newSecurityTestEnv(t)
	admin := mustCreateRoleUser(t, db, "admin", model.RoleAdmin, "*", "active")
	token := mustToken(t, app, admin)

	rec, resp := doUpload(t, router, "empty.zip", nil, token)
	if rec.Code != http.StatusBadRequest || resp.Message != "包文件为空" {
		t.Fatalf("空文件应 400，实际 %d / %q", rec.Code, resp.Message)
	}
	if n := countPackageFiles(t, app.Cfg.Storage.PackageDir); n != 0 {
		t.Fatalf("空文件不应写盘，实际 %d 个文件", n)
	}
}

func TestSweepStaleUploadsRemovesOnlyStaleTemps(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "200601")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	stale := filepath.Join(dir, "abc.zip"+uploadTempMarker+"deadbeef")
	fresh := filepath.Join(dir, "def.zip"+uploadTempMarker+"cafebabe")
	committed := filepath.Join(dir, "ghi.zip")
	for _, p := range []string{stale, fresh, committed} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("调整 mtime 失败: %v", err)
	}

	if removed := sweepStaleUploads(root, time.Hour); removed != 1 {
		t.Fatalf("应只清理 1 个残留临时文件，实际 %d", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("过期临时文件应被删除")
	}
	for _, p := range []string{fresh, committed} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("不应删除 %s: %v", filepath.Base(p), err)
		}
	}
}

func TestUploadPayloadEstimate(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{-1, 0},
		{0, 0},
		{uploadMaxOverhead, 0},
		{uploadMaxOverhead + 100, 100},
	}
	for _, c := range cases {
		if got := uploadPayloadEstimate(c.in); got != c.want {
			t.Fatalf("uploadPayloadEstimate(%d) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

var _ io.Reader = (*errReader)(nil)
