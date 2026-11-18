package api

import (
	"bytes"
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
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/packrun/packrun/internal/model"
)

const maxPackageSize = 200 << 20 // 单包上限 200MB，与 NG 侧 client_max_body_size 保持一致

// packageRetentionDays 最近上传仅保留含当天在内的最近 3 天记录，超期自动清理。
const packageRetentionDays = 3

// zipMagic 是 ZIP/JAR 的本地文件头签名。
var zipMagic = []byte{'P', 'K', 0x03, 0x04}

func (a *App) uploadPackage(c *gin.Context) {
	if currentUser(c).Role == model.RoleAuditor {
		forbidden(c, "审计角色不可上传安装包")
		return
	}
	// 声明长度已超限时直接拒绝，不必先读完整个请求体。
	if c.Request.ContentLength > int64(maxPackageSize)+uploadMaxOverhead {
		fail(c, http.StatusRequestEntityTooLarge, CodeBadRequest,
			fmt.Sprintf("包大小超过上限 %d MB", maxPackageSize>>20))
		return
	}

	// 流式解析 multipart：文件段直接写进仓库目录，
	// 不再经 ParseMultipartForm 先落系统临时目录再复制一次（省一半磁盘 IO 与空间）。
	reader, err := c.Request.MultipartReader()
	if err != nil {
		badRequest(c, "上传请求格式不正确，请重新选择文件")
		return
	}
	part, err := nextFilePart(reader)
	if err != nil {
		writeUploadFailure(c, err)
		return
	}
	if part == nil {
		badRequest(c, "请选择要上传的包文件")
		return
	}
	// 这里不做 defer part.Close()：Part.Close 会把该段剩余字节全部读完，
	// 遇到「超限」「配额已满」等提前返回时会被迫读完整包才响应。
	// 交给 HTTP 服务端按 256KB 上限排空后关连接即可。

	filename := part.FileName()
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".zip" && ext != ".jar" {
		badRequest(c, "仅支持 .zip 与 .jar 包")
		return
	}
	name, err := sanitizePackageName(filename)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	// 不信任扩展名：先读文件头确认确实是 ZIP/JAR，已读字节稍后接回写入流。
	header := make([]byte, len(zipMagic))
	n, err := io.ReadFull(part, header)
	if n == 0 {
		badRequest(c, "包文件为空")
		return
	}
	if err != nil || !bytes.Equal(header[:n], zipMagic) {
		badRequest(c, "文件内容不是有效的 ZIP/JAR")
		return
	}

	// 仓库容量预检：按请求体估算先拦一次，写入后按实际占用复核。
	if tooLarge := a.packageQuotaExceeded(uploadPayloadEstimate(c.Request.ContentLength)); tooLarge {
		fail(c, http.StatusInsufficientStorage, CodeInternalError, "包仓库容量已满")
		return
	}

	key := filepath.Join(time.Now().Format("200601"), randomHex(8)+ext)
	dst := filepath.Join(a.Cfg.Storage.PackageDir, key)
	written, sum, err := savePackageFile(dst, io.MultiReader(bytes.NewReader(header[:n]), part), maxPackageSize)
	if err != nil {
		writeUploadFailure(c, err)
		return
	}
	if tooLarge := a.packageDirExceedsQuota(); tooLarge {
		os.Remove(dst)
		fail(c, http.StatusInsufficientStorage, CodeInternalError, "包仓库容量已满")
		return
	}

	pkg := model.Package{
		Name:       name,
		Kind:       strings.TrimPrefix(ext, "."),
		Size:       written,
		SHA256:     sum,
		StorageKey: key,
		UploaderID: currentUser(c).ID,
	}
	if err := a.DB.Create(&pkg).Error; err != nil {
		os.Remove(dst)
		internal(c, "登记包记录失败")
		return
	}

	a.audit(c, "package.upload", pkg.Name, "ok", "", fmt.Sprintf("%s %d 字节 sha256=%s", pkg.Kind, pkg.Size, pkg.SHA256), false)
	ok(c, pkg)
}

// packageQuotaExceeded 判断包仓库当前占用加上本次字节数是否超过配置上限。
func (a *App) packageQuotaExceeded(incoming int64) bool {
	if a.Cfg.Storage.MaxTotalMB <= 0 {
		return false
	}
	used, err := packageDirBytes(a.Cfg.Storage.PackageDir)
	if err != nil {
		// 统计失败时放行，避免因目录暂时不可读而阻断上传；写入后的复核同样安全。
		return false
	}
	return used+incoming > int64(a.Cfg.Storage.MaxTotalMB)<<20
}

// packageDirExceedsQuota 在写入完成后按目录实际占用复核容量。
func (a *App) packageDirExceedsQuota() bool {
	if a.Cfg.Storage.MaxTotalMB <= 0 {
		return false
	}
	used, err := packageDirBytes(a.Cfg.Storage.PackageDir)
	if err != nil {
		return false
	}
	return used > int64(a.Cfg.Storage.MaxTotalMB)<<20
}

// packageDirBytes 递归统计目录内普通文件的总字节数。
func packageDirBytes(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

// sanitizePackageName 取文件名最后一段并替换路径分隔符与控制字符，保留可读中文。
func sanitizePackageName(raw string) (string, error) {
	base := filepath.Base(raw)
	var b strings.Builder
	for _, r := range base {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	name := strings.TrimSpace(b.String())
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("包文件名称不合法")
	}
	return name, nil
}

// deletePackage 移除未被发布任务引用的安装包，同时删除仓库文件，释放容量。
func (a *App) deletePackage(c *gin.Context) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可移除安装包")
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	var pkg model.Package
	if err := a.DB.First(&pkg, id).Error; err != nil {
		notFound(c, "安装包不存在")
		return
	}
	if u.Role != model.RoleAdmin && pkg.UploaderID != u.ID {
		forbidden(c, "只能移除自己上传的安装包")
		return
	}

	// 已被发布/回滚任务使用的包必须保留，否则对应批次无法回滚。
	var used int64
	if err := a.DB.Model(&model.Task{}).Where("package_id = ?", pkg.ID).Count(&used).Error; err != nil {
		internal(c, "检查安装包引用失败")
		return
	}
	if used > 0 {
		badRequest(c, "该安装包已被发布任务使用，无法移除")
		return
	}

	if pkg.StorageKey != "" {
		if err := os.Remove(filepath.Join(a.Cfg.Storage.PackageDir, pkg.StorageKey)); err != nil && !os.IsNotExist(err) {
			internal(c, "删除安装包文件失败")
			return
		}
	}
	if err := a.DB.Delete(&pkg).Error; err != nil {
		internal(c, "删除安装包记录失败")
		return
	}

	a.audit(c, "package.delete", pkg.Name, "ok", "", fmt.Sprintf("%s %d 字节 sha256=%s", pkg.Kind, pkg.Size, pkg.SHA256), false)
	ok(c, gin.H{"deleted": true})
}

func (a *App) listPackages(c *gin.Context) {
	var list []model.Package
	if err := a.DB.Where("created_at >= ?", packageRetentionCutoff(time.Now())).
		Order("id desc").Limit(200).Find(&list).Error; err != nil {
		internal(c, "查询包列表失败")
		return
	}
	ok(c, list)
}

// packageRetentionCutoff 返回保留窗口起点：当天 0 点往前推 packageRetentionDays-1 天（含当天共 3 天）。
func packageRetentionCutoff(now time.Time) time.Time {
	y, m, d := now.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return start.AddDate(0, 0, -(packageRetentionDays - 1))
}

// purgeExpiredPackages 清理超过保留期的上传记录及其仓库文件；
// 仍被发布/回滚任务引用的包保留，避免历史批次无法回滚。
func purgeExpiredPackages(app *App) {
	var expired []model.Package
	if err := app.DB.Where("created_at < ?", packageRetentionCutoff(time.Now())).Find(&expired).Error; err != nil {
		log.Printf("[packages] 查询过期上传记录失败: %v", err)
		return
	}
	removed := 0
	for _, pkg := range expired {
		var used int64
		if err := app.DB.Model(&model.Task{}).Where("package_id = ?", pkg.ID).Count(&used).Error; err != nil {
			log.Printf("[packages] 检查包引用失败 id=%d: %v", pkg.ID, err)
			continue
		}
		if used > 0 {
			continue
		}
		if pkg.StorageKey != "" {
			if err := os.Remove(filepath.Join(app.Cfg.Storage.PackageDir, pkg.StorageKey)); err != nil && !os.IsNotExist(err) {
				log.Printf("[packages] 删除过期包文件失败 id=%d: %v", pkg.ID, err)
				continue
			}
		}
		if err := app.DB.Delete(&pkg).Error; err != nil {
			log.Printf("[packages] 删除过期包记录失败 id=%d: %v", pkg.ID, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Printf("[packages] 已清理 %d 个超过 %d 天的上传包", removed, packageRetentionDays)
	}
}

// StartPackageRetentionWorker 启动后立即清理一次，之后按 interval 周期清理过期上传记录。
func StartPackageRetentionWorker(app *App, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		log.Printf("[packages] 上传记录保留 %d 天，超期自动清理已启动", packageRetentionDays)
		maintainPackageStore(app)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			maintainPackageStore(app)
		}
	}()
}

// maintainPackageStore 执行一次仓库维护：清理过期上传记录，顺带回收异常退出残留的临时文件。
func maintainPackageStore(app *App) {
	purgeExpiredPackages(app)
	// 一小时以内的临时文件可能属于正在进行的上传，不动。
	if n := sweepStaleUploads(app.Cfg.Storage.PackageDir, time.Hour); n > 0 {
		log.Printf("[packages] 已清理 %d 个残留的上传临时文件", n)
	}
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
