package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

// ---------- 视图 ----------

// ProjectDirView 目录视图：附加服务器名与环境，供工作台直接渲染。
type ProjectDirView struct {
	model.Directory
	ServerName string `json:"serverName"`
	ServerEnv  string `json:"serverEnv"`
}

// ProjectMemberView 成员视图。
type ProjectMemberView struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

// ProjectView 项目详情（列表复用）。
type ProjectView struct {
	model.Project
	ScriptName string              `json:"scriptName"`
	Dirs       []ProjectDirView    `json:"dirs"`
	Members    []ProjectMemberView `json:"members"`
}

// TaskBrief 历史条目内的单目录任务摘要。
type TaskBrief struct {
	ID         uint   `json:"id"`
	TaskNo     string `json:"taskNo"`
	ServerName string `json:"serverName"`
	TargetPath string `json:"targetPath"`
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	Progress   int    `json:"progress"`
	Message    string `json:"message"`
}

// HistoryItem 项目发布历史条目：同批次（BatchNo）聚合为一条记录。
type HistoryItem struct {
	BatchNo        string      `json:"batchNo"`
	Kind           string      `json:"kind"`
	ProjectVersion string      `json:"projectVersion"`
	PackageID      uint        `json:"packageId"`
	PackageName    string      `json:"packageName"`
	PackageSize    int64       `json:"packageSize"`
	SHA256         string      `json:"sha256"`
	Operator       string      `json:"operator"`
	Remark         string      `json:"remark"`
	Status         string      `json:"status"` // running | success | failed | canceled
	CreatedAt      time.Time   `json:"createdAt"`
	Tasks          []TaskBrief `json:"tasks"`
}

// ---------- 访问控制 ----------

// canReadProject 管理员、项目 Owner 或项目成员可读。
// Owner 即使没有 project_members 记录也始终可读，避免「自己建的项目看不到」。
func (a *App) canReadProject(u *model.User, projectID uint) bool {
	if u == nil {
		return false
	}
	var p model.Project
	if err := a.DB.Select("id", "owner_id").First(&p, projectID).Error; err != nil {
		return false
	}
	if u.Role == model.RoleAdmin || p.OwnerID == u.ID {
		return true
	}
	var n int64
	a.DB.Model(&model.ProjectMember{}).
		Where("project_id = ? AND user_id = ?", projectID, u.ID).Count(&n)
	return n > 0
}

// canManageProject 管理员或项目 Owner 可改配置/删除。
func canManageProject(u *model.User, p model.Project) bool {
	return u.Role == model.RoleAdmin || p.OwnerID == u.ID
}

// loadProjectDirs 读取项目绑定的目录（含服务器信息）。
func (a *App) loadProjectDirs(projectID uint) ([]ProjectDirView, error) {
	var dirIDs []uint
	if err := a.DB.Model(&model.ProjectDirectory{}).Where("project_id = ?", projectID).
		Order("directory_id asc").Pluck("directory_id", &dirIDs).Error; err != nil {
		return nil, err
	}
	views := make([]ProjectDirView, 0, len(dirIDs))
	for _, id := range dirIDs {
		var d model.Directory
		if err := a.DB.First(&d, id).Error; err != nil {
			continue // 目录可能已被删除，跳过失效绑定
		}
		v := ProjectDirView{Directory: d}
		var s model.Server
		if err := a.DB.First(&s, d.ServerID).Error; err == nil {
			v.ServerName = s.Name
			v.ServerEnv = s.Env
		}
		views = append(views, v)
	}
	return views, nil
}

// loadProjectMembers 读取项目成员。
// 非管理员 Owner 作为隐式成员展示，不要求 project_members 里有记录。
func (a *App) loadProjectMembers(projectID uint) []ProjectMemberView {
	var p model.Project
	_ = a.DB.Select("id", "owner_id").First(&p, projectID).Error

	var userIDs []uint
	a.DB.Model(&model.ProjectMember{}).Where("project_id = ?", projectID).
		Order("user_id asc").Pluck("user_id", &userIDs)
	views := make([]ProjectMemberView, 0, len(userIDs)+1)
	if p.OwnerID != 0 {
		var owner model.User
		if err := a.DB.Select("id", "username", "display_name", "role").First(&owner, p.OwnerID).Error; err == nil && owner.Role != model.RoleAdmin {
			views = append(views, ProjectMemberView{
				ID: owner.ID, Username: owner.Username, DisplayName: owner.DisplayName, Role: owner.Role,
			})
		}
	}
	for _, id := range userIDs {
		if id == p.OwnerID {
			continue // Owner 已在上面展示，避免重复
		}
		var u model.User
		if err := a.DB.First(&u, id).Error; err != nil {
			continue
		}
		views = append(views, ProjectMemberView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role})
	}
	return views
}

func (a *App) projectView(p model.Project) (*ProjectView, error) {
	dirs, err := a.loadProjectDirs(p.ID)
	if err != nil {
		return nil, err
	}
	v := &ProjectView{Project: p, Dirs: dirs, Members: a.loadProjectMembers(p.ID)}
	if p.ScriptID != nil && *p.ScriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *p.ScriptID).Error; err == nil {
			v.ScriptName = s.Name
		}
	}
	return v, nil
}

// ---------- CRUD ----------

type createProjectReq struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	ScriptID    *uint  `json:"scriptId"`
}

func (a *App) listProjects(c *gin.Context) {
	u := currentUser(c)
	q := a.DB.Model(&model.Project{})
	if u.Role != model.RoleAdmin {
		var ids []uint
		a.DB.Model(&model.ProjectMember{}).Where("user_id = ?", u.ID).Pluck("project_id", &ids)
		if len(ids) == 0 {
			q = q.Where("owner_id = ?", u.ID)
		} else {
			// Owner 即使没有成员记录也应看到自己创建的项目。
			q = q.Where("owner_id = ? OR id IN ?", u.ID, ids)
		}
	}
	var list []model.Project
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		internal(c, "查询项目失败")
		return
	}
	views := make([]ProjectView, 0, len(list))
	for _, p := range list {
		v, err := a.projectView(p)
		if err != nil {
			internal(c, "组装项目详情失败")
			return
		}
		views = append(views, *v)
	}
	ok(c, views)
}

func (a *App) createProject(c *gin.Context) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可创建项目")
		return
	}
	var req createProjectReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		badRequest(c, "请填写项目名称")
		return
	}
	code := strings.ToLower(strings.TrimSpace(req.Code))
	if code == "" {
		code = "project"
	}
	code = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, code)
	if code == "" {
		badRequest(c, "项目标识仅支持小写字母、数字与连字符")
		return
	}
	var dup int64
	a.DB.Model(&model.Project{}).Where("code = ?", code).Count(&dup)
	if dup > 0 {
		fail(c, 409, CodeConflict, "项目标识已存在："+code)
		return
	}

	var scriptID *uint
	if req.ScriptID != nil && *req.ScriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *req.ScriptID).Error; err != nil {
			badRequest(c, "部署后脚本不存在")
			return
		}
		if s.Phase != model.PhasePostDeploy {
			badRequest(c, "部署后脚本阶段必须为 postdeploy")
			return
		}
		scriptID = req.ScriptID
	}

	p := model.Project{
		Name: strings.TrimSpace(req.Name), Code: code,
		Description: strings.TrimSpace(req.Description),
		OwnerID:     u.ID, ScriptID: scriptID, Version: "v0.0.0", Status: "active",
	}
	if err := a.DB.Create(&p).Error; err != nil {
		internal(c, "创建项目失败")
		return
	}
	a.audit(c, "project.create", p.Name, "ok", "", "标识="+p.Code, false)
	v, err := a.projectView(p)
	if err != nil {
		internal(c, "组装项目详情失败")
		return
	}
	ok(c, v)
}

func (a *App) getProject(c *gin.Context) {
	u := currentUser(c)
	var p model.Project
	if err := a.DB.First(&p, c.Param("id")).Error; err != nil {
		notFound(c, "项目不存在")
		return
	}
	if !a.canReadProject(u, p.ID) {
		forbidden(c, "尚未被授权访问该项目")
		return
	}
	v, err := a.projectView(p)
	if err != nil {
		internal(c, "组装项目详情失败")
		return
	}
	ok(c, v)
}

// policyReq 发布策略：同步应用到项目绑定的全部目录（仅管理员）。
type policyReq struct {
	BackupBeforeDeploy *bool `json:"backupBeforeDeploy"`
	AllowOverwrite     *bool `json:"allowOverwrite"`
	KeepReleases       *int  `json:"keepReleases"`
}

type updateProjectReq struct {
	Name         *string    `json:"name"`
	Description  *string    `json:"description"`
	ScriptID     *uint      `json:"scriptId"` // 0 表示解绑部署后脚本
	DirectoryIDs *[]uint    `json:"directoryIds"`
	MemberIDs    *[]uint    `json:"memberIds"`
	Policy       *policyReq `json:"policy"`
}

func (a *App) updateProject(c *gin.Context) {
	u := currentUser(c)
	var p model.Project
	if err := a.DB.First(&p, c.Param("id")).Error; err != nil {
		notFound(c, "项目不存在")
		return
	}
	if !canManageProject(u, p) {
		forbidden(c, "仅管理员或项目创建者可修改项目设置")
		return
	}
	var req updateProjectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}

	// 成员与发布策略都是全局配置资源，仅管理员可提交。
	if req.MemberIDs != nil && u.Role != model.RoleAdmin {
		forbidden(c, "仅管理员可调整项目成员")
		return
	}
	if req.Policy != nil && u.Role != model.RoleAdmin {
		forbidden(c, "仅管理员可调整发布策略")
		return
	}

	// 目录绑定：先整体校验，任一目录不存在或超出数据范围都拒绝，且不改动既有绑定。
	var bindings []model.ProjectDirectory
	if req.DirectoryIDs != nil {
		seen := map[uint]bool{}
		bindings = make([]model.ProjectDirectory, 0, len(*req.DirectoryIDs))
		for _, id := range *req.DirectoryIDs {
			if id == 0 || seen[id] {
				continue
			}
			var dir model.Directory
			if err := a.DB.First(&dir, id).Error; err != nil {
				forbidden(c, "目录不存在或无权访问")
				return
			}
			if !checkServerScope(u, dir.ServerID) {
				forbidden(c, "目录不存在或无权访问")
				return
			}
			seen[id] = true
			bindings = append(bindings, model.ProjectDirectory{ProjectID: p.ID, DirectoryID: id})
		}
	}

	updates := map[string]any{}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updates["description"] = strings.TrimSpace(*req.Description)
	}
	if req.ScriptID != nil {
		if *req.ScriptID == 0 {
			updates["script_id"] = nil
		} else {
			var s model.Script
			if err := a.DB.First(&s, *req.ScriptID).Error; err != nil {
				badRequest(c, "部署后脚本不存在")
				return
			}
			if s.Phase != model.PhasePostDeploy {
				badRequest(c, "部署后脚本阶段必须为 postdeploy")
				return
			}
			updates["script_id"] = *req.ScriptID
		}
	}
	if len(updates) > 0 {
		a.DB.Model(&p).Updates(updates)
	}

	// 目录绑定：校验全部通过后整体替换。
	if req.DirectoryIDs != nil {
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("project_id = ?", p.ID).Delete(&model.ProjectDirectory{}).Error; err != nil {
				return err
			}
			if len(bindings) == 0 {
				return nil
			}
			return tx.Create(&bindings).Error
		}); err != nil {
			internal(c, "更新目录绑定失败")
			return
		}
	}

	// 成员授权：整体替换（成员即子用户可见范围）
	if req.MemberIDs != nil {
		a.DB.Where("project_id = ?", p.ID).Delete(&model.ProjectMember{})
		members := make([]model.ProjectMember, 0, len(*req.MemberIDs))
		for _, id := range *req.MemberIDs {
			if id <= 0 || id == p.OwnerID {
				continue
			}
			members = append(members, model.ProjectMember{ProjectID: p.ID, UserID: id})
		}
		if len(members) > 0 {
			a.DB.Create(&members)
		}
	}

	// 发布策略：同步写入绑定目录（目录策略为全局资源，仅管理员可改）
	if req.Policy != nil {
		dirIDs, _ := a.loadProjectDirs(p.ID)
		for _, d := range dirIDs {
			up := map[string]any{}
			if req.Policy.BackupBeforeDeploy != nil {
				up["backup_before_deploy"] = *req.Policy.BackupBeforeDeploy
			}
			if req.Policy.AllowOverwrite != nil {
				up["allow_overwrite"] = *req.Policy.AllowOverwrite
			}
			if req.Policy.KeepReleases != nil {
				k := *req.Policy.KeepReleases
				if k < 1 {
					k = 1
				}
				if k > 20 {
					k = 20
				}
				up["keep_releases"] = k
			}
			if len(up) > 0 {
				a.DB.Model(&model.Directory{}).Where("id = ?", d.ID).Updates(up)
			}
		}
	}

	a.audit(c, "project.update", p.Name, "ok", "", "更新项目设置", false)
	// 重新读取，确保响应包含刚保存的字段（含 scriptId）。
	if err := a.DB.First(&p, p.ID).Error; err != nil {
		internal(c, "读取项目失败")
		return
	}
	v, err := a.projectView(p)
	if err != nil {
		internal(c, "组装项目详情失败")
		return
	}
	ok(c, v)
}

func (a *App) deleteProject(c *gin.Context) {
	u := currentUser(c)
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可删除项目")
		return
	}
	var p model.Project
	if err := a.DB.First(&p, c.Param("id")).Error; err != nil {
		notFound(c, "项目不存在")
		return
	}
	if !canManageProject(u, p) {
		forbidden(c, "仅管理员或项目创建者可删除项目")
		return
	}
	a.DB.Where("project_id = ?", p.ID).Delete(&model.ProjectDirectory{})
	a.DB.Where("project_id = ?", p.ID).Delete(&model.ProjectMember{})
	a.DB.Delete(&p)
	a.audit(c, "project.delete", p.Name, "ok", "", "删除项目（历史任务保留）", false)
	ok(c, gin.H{"deleted": true})
}

// ---------- 发布 / 回滚 ----------

type publishReq struct {
	PackageID   uint   `json:"packageId" binding:"required"`
	PreScriptID *uint  `json:"preScriptId"`
	ScriptID    *uint  `json:"scriptId"`
	OTP         string `json:"otp"`
}

type rollbackReq struct {
	BatchNo string `json:"batchNo" binding:"required"`
	OTP     string `json:"otp"`
}

const batchRunningStates = "'" + model.TaskPending + "','" + model.TaskUploading + "','" + model.TaskVerifying + "','" + model.TaskExecuting + "'"

// projectHasRunningBatch 项目是否仍有进行中的发布批次。
func (a *App) projectHasRunningBatch(projectID uint) bool {
	var n int64
	a.DB.Model(&model.Task{}).
		Where("project_id = ? AND status IN ("+batchRunningStates+")", projectID).Count(&n)
	return n > 0
}

// checkPublishPrerequisites 发布/回滚共用校验：成员权限、目录绑定、服务器范围、OTP 再认证。
// 返回 (项目绑定的目录视图, 是否确实通过 OTP 校验, 是否通过)。
func (a *App) checkPublishPrerequisites(c *gin.Context, u *model.User, p model.Project, otp string) ([]ProjectDirView, bool, bool) {
	if u.Role == model.RoleAuditor {
		forbidden(c, "审计角色不可发起发布")
		return nil, false, false
	}
	if !a.canReadProject(u, p.ID) {
		forbidden(c, "尚未被授权操作该项目")
		return nil, false, false
	}
	if a.projectHasRunningBatch(p.ID) {
		badRequest(c, "该项目仍有发布任务进行中，请等待完成或取消后再试")
		return nil, false, false
	}
	dirs, err := a.loadProjectDirs(p.ID)
	if err != nil {
		internal(c, "读取项目目录失败")
		return nil, false, false
	}
	if len(dirs) == 0 {
		badRequest(c, "项目尚未绑定更新目录，请先在项目设置中绑定")
		return nil, false, false
	}

	needOTP := false
	for _, d := range dirs {
		if !checkServerScope(u, d.ServerID) {
			forbidden(c, "无权操作服务器："+d.ServerName)
			return nil, false, false
		}
		if d.ServerEnv == model.EnvProd {
			needOTP = true
		}
	}
	if !needOTP {
		return dirs, false, true
	}
	if passed, msg := a.verifyStepUpOTP(u, otp); !passed {
		a.audit(c, "project.publish", p.Name, "fail", "", msg, false)
		fail(c, 401, CodeOTPRequired, msg)
		return nil, false, false
	}
	return dirs, true, true
}

// createBatchTasks 为一批目录创建同批次任务并异步执行。
func (a *App) createBatchTasks(c *gin.Context, u *model.User, p model.Project,
	pkg model.Package, dirs []ProjectDirView, kind, batchNo, version, remark string,
	preScript, script *model.Script, otpVerified bool) []model.Task {

	tasks := make([]model.Task, 0, len(dirs))
	for _, d := range dirs {
		task := model.Task{
			TaskNo:         newTaskNo(),
			ServerID:       d.ServerID,
			ServerName:     d.ServerName,
			DirectoryID:    d.ID,
			TargetPath:     d.Path,
			PackageID:      pkg.ID,
			PackageName:    pkg.Name,
			PreScriptID:    scriptIDOrNil(preScript),
			ScriptID:       scriptIDOrNil(script),
			ProjectID:      p.ID,
			BatchNo:        batchNo,
			Kind:           kind,
			ProjectVersion: version,
			Remark:         remark,
			OperatorID:     u.ID,
			Operator:       u.Username,
			Status:         model.TaskPending,
			Stage:          "queued",
			Message:        "任务已受理，等待执行",
			OTPVerified:    otpVerified,
		}
		if preScript != nil {
			task.PreScriptName = preScript.Name
			task.PreScriptVersion = preScript.Version
		}
		if script != nil {
			task.ScriptName = script.Name
			task.ScriptVersion = script.Version
		}
		if err := a.DB.Create(&task).Error; err != nil {
			continue
		}
		// 与部署向导一致：脚本内容随任务固化，批量受理后编辑脚本不影响本批次。
		a.snapshotTaskScripts(&task, preScript, script)
		tasks = append(tasks, task)
		go a.runTask(task.ID)
	}
	return tasks
}

func scriptIDOrNil(s *model.Script) *uint {
	if s == nil {
		return nil
	}
	id := s.ID
	return &id
}

// loadPublishScripts 校验可选脚本并加载（与 createTask 的阶段约束一致）。
func (a *App) loadPublishScripts(c *gin.Context, preScriptID, scriptID *uint) (*model.Script, *model.Script, bool) {
	var preScript, script *model.Script
	if preScriptID != nil && *preScriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *preScriptID).Error; err != nil {
			badRequest(c, "前置校验脚本不存在")
			return nil, nil, false
		}
		if s.Phase != model.PhasePreCheck {
			badRequest(c, "前置脚本阶段必须为 precheck")
			return nil, nil, false
		}
		preScript = &s
	}
	if scriptID != nil && *scriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *scriptID).Error; err != nil {
			badRequest(c, "部署脚本不存在")
			return nil, nil, false
		}
		if s.Phase != model.PhasePostDeploy {
			badRequest(c, "后置脚本阶段必须为 postdeploy")
			return nil, nil, false
		}
		script = &s
	}
	return preScript, script, true
}

func newBatchNo() string {
	return "PR" + time.Now().Format("20060102150405") + strings.ToUpper(randomHex(3))
}

// bumpVersion 版本号递进：v a.b.c → v a.b.(c+1)，无法解析时从 v0.0.1 起步。
func bumpVersion(v string) string {
	var a, b, cc int
	if _, err := fmt.Sscanf(v, "v%d.%d.%d", &a, &b, &cc); err != nil {
		return "v0.0.1"
	}
	return fmt.Sprintf("v%d.%d.%d", a, b, cc+1)
}

func (a *App) publishProject(c *gin.Context) {
	u := currentUser(c)
	var p model.Project
	if err := a.DB.First(&p, c.Param("id")).Error; err != nil {
		notFound(c, "项目不存在")
		return
	}
	var req publishReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请选择要发布的安装包")
		return
	}
	if req.OTP == "" {
		req.OTP = c.GetHeader("X-OTP")
	}

	dirs, otpVerified, pass := a.checkPublishPrerequisites(c, u, p, req.OTP)
	if !pass {
		return
	}
	var pkg model.Package
	if err := a.DB.First(&pkg, req.PackageID).Error; err != nil {
		badRequest(c, "安装包不存在")
		return
	}
	if !canUsePackage(u, &pkg) {
		forbidden(c, "只能使用自己上传的安装包")
		return
	}
	// 确认包文件仍存在于仓库中
	if _, err := os.Stat(filepath.Join(a.Cfg.Storage.PackageDir, pkg.StorageKey)); err != nil {
		badRequest(c, "安装包文件已丢失，请重新上传")
		return
	}

	batchNo := newBatchNo()
	version := bumpVersion(p.Version)
	preScript, script, pass := a.loadPublishScripts(c, req.PreScriptID, req.ScriptID)
	if !pass {
		return
	}
	// 请求未携带 scriptId 时才沿用项目默认；显式传 0 表示本次不执行脚本。
	if req.ScriptID == nil && script == nil && p.ScriptID != nil && *p.ScriptID > 0 {
		var s model.Script
		if err := a.DB.First(&s, *p.ScriptID).Error; err == nil && s.Phase == model.PhasePostDeploy {
			script = &s
		}
	}
	// 资源滥用防护：整批任务必须一次性落在单用户配额内，超限时不创建任何任务。
	if msg, pass := a.checkPendingTaskLimit(u, len(dirs)); !pass {
		a.audit(c, "project.publish", p.Name, "fail", "", msg, otpVerified)
		fail(c, http.StatusTooManyRequests, CodeDeployFailed, msg)
		return
	}
	tasks := a.createBatchTasks(c, u, p, pkg, dirs, model.TaskKindDeploy, batchNo, version,
		"", preScript, script, otpVerified)
	if len(tasks) == 0 {
		internal(c, "创建发布任务失败")
		return
	}

	a.audit(c, "project.publish", p.Name, "ok", batchNo,
		fmt.Sprintf("版本=%s 包=%s 目录数=%d", version, pkg.Name, len(tasks)), otpVerified)

	// 版本号在批次全部成功后由 settleProjectBatch 落定，失败不推进
	ok(c, gin.H{"batchNo": batchNo, "version": version, "tasks": tasks})
}

func (a *App) rollbackProject(c *gin.Context) {
	u := currentUser(c)
	var p model.Project
	if err := a.DB.First(&p, c.Param("id")).Error; err != nil {
		notFound(c, "项目不存在")
		return
	}
	var req rollbackReq
	if err := c.ShouldBindJSON(&req); err != nil || req.BatchNo == "" {
		badRequest(c, "请指定要回滚的发布批次")
		return
	}
	if req.OTP == "" {
		req.OTP = c.GetHeader("X-OTP")
	}

	// 源批次必须属于本项目
	var source model.Task
	if err := a.DB.Where("project_id = ? AND batch_no = ?", p.ID, req.BatchNo).
		Order("id asc").First(&source).Error; err != nil {
		notFound(c, "发布批次不存在")
		return
	}
	if p.Version == source.ProjectVersion {
		badRequest(c, "项目当前已处于该版本，无需回滚")
		return
	}

	dirs, otpVerified, pass := a.checkPublishPrerequisites(c, u, p, req.OTP)
	if !pass {
		return
	}
	var pkg model.Package
	if err := a.DB.First(&pkg, source.PackageID).Error; err != nil {
		badRequest(c, "该批次的安装包已被回收（超出保留版本数），无法回滚")
		return
	}
	if _, err := os.Stat(filepath.Join(a.Cfg.Storage.PackageDir, pkg.StorageKey)); err != nil {
		badRequest(c, "该批次的安装包文件已被清理，无法回滚")
		return
	}

	batchNo := newBatchNo()
	remark := "回滚自 " + p.Version
	var preScript, script *model.Script
	if source.PreScriptID != nil {
		var s model.Script
		if err := a.DB.First(&s, *source.PreScriptID).Error; err == nil {
			preScript = &s
		}
	}
	if source.ScriptID != nil {
		var s model.Script
		if err := a.DB.First(&s, *source.ScriptID).Error; err == nil {
			script = &s
		}
	}
	// 资源滥用防护：整批回滚任务必须一次性落在单用户配额内。
	if msg, pass := a.checkPendingTaskLimit(u, len(dirs)); !pass {
		a.audit(c, "project.rollback", p.Name, "fail", "", msg, otpVerified)
		fail(c, http.StatusTooManyRequests, CodeDeployFailed, msg)
		return
	}
	tasks := a.createBatchTasks(c, u, p, pkg, dirs, model.TaskKindRollback, batchNo,
		source.ProjectVersion, remark, preScript, script, otpVerified)
	if len(tasks) == 0 {
		internal(c, "创建回滚任务失败")
		return
	}

	a.audit(c, "project.rollback", p.Name, "ok", batchNo,
		fmt.Sprintf("回滚至=%s 包=%s 目录数=%d", source.ProjectVersion, pkg.Name, len(tasks)), otpVerified)

	// 版本回退同样在批次全部成功后由 settleProjectBatch 落定
	ok(c, gin.H{"batchNo": batchNo, "version": source.ProjectVersion, "tasks": tasks})
}

// ---------- 历史 ----------

// aggregateStatus 聚合批次内全部任务的状态。
func aggregateStatus(tasks []TaskBrief) string {
	running, failed, success := 0, 0, 0
	for _, t := range tasks {
		switch t.Status {
		case model.TaskSuccess:
			success++
		case model.TaskFailed:
			failed++
		case model.TaskCanceled:
			// 归入未完成
		default:
			running++
		}
	}
	if running > 0 {
		return "running"
	}
	if failed > 0 {
		return "failed"
	}
	if success == len(tasks) {
		return "success"
	}
	return "canceled"
}

func (a *App) projectHistory(c *gin.Context) {
	u := currentUser(c)
	var p model.Project
	if err := a.DB.First(&p, c.Param("id")).Error; err != nil {
		notFound(c, "项目不存在")
		return
	}
	if !a.canReadProject(u, p.ID) {
		forbidden(c, "尚未被授权查看该项目")
		return
	}

	var tasks []model.Task
	if err := a.DB.Where("project_id = ?", p.ID).Order("id desc").Find(&tasks).Error; err != nil {
		internal(c, "查询发布历史失败")
		return
	}

	items := make([]HistoryItem, 0)
	index := map[string]int{}
	pkgCache := map[uint]model.Package{}
	for _, t := range tasks {
		if t.BatchNo == "" {
			continue
		}
		i, seen := index[t.BatchNo]
		if !seen {
			i = len(items)
			index[t.BatchNo] = i
			item := HistoryItem{
				BatchNo: t.BatchNo, Kind: t.Kind, ProjectVersion: t.ProjectVersion,
				PackageID: t.PackageID, PackageName: t.PackageName,
				Operator: t.Operator, CreatedAt: t.CreatedAt, Remark: t.Remark,
			}
			if pkg, ok := pkgCache[t.PackageID]; ok {
				item.PackageSize = pkg.Size
				item.SHA256 = pkg.SHA256
			} else {
				var pkg model.Package
				if err := a.DB.First(&pkg, t.PackageID).Error; err == nil {
					pkgCache[t.PackageID] = pkg
					item.PackageSize = pkg.Size
					item.SHA256 = pkg.SHA256
				}
			}
			items = append(items, item)
		}
		items[i].Tasks = append(items[i].Tasks, TaskBrief{
			ID: t.ID, TaskNo: t.TaskNo, ServerName: t.ServerName, TargetPath: t.TargetPath,
			Status: t.Status, Stage: t.Stage, Progress: t.Progress, Message: t.Message,
		})
	}
	for i := range items {
		items[i].Status = aggregateStatus(items[i].Tasks)
	}
	ok(c, items)
}

// settleProjectBatch 批次全部终态后结算：全部成功才把批次版本落为项目当前版本。
func (a *App) settleProjectBatch(projectID uint, batchNo string) {
	if projectID == 0 || batchNo == "" {
		return
	}
	var tasks []model.Task
	if err := a.DB.Where("project_id = ? AND batch_no = ?", projectID, batchNo).Find(&tasks).Error; err != nil || len(tasks) == 0 {
		return
	}
	for _, t := range tasks {
		switch t.Status {
		case model.TaskSuccess, model.TaskFailed, model.TaskCanceled:
		default:
			return // 仍有进行中的任务
		}
	}
	allSuccess := true
	for _, t := range tasks {
		if t.Status != model.TaskSuccess {
			allSuccess = false
			break
		}
	}
	if !allSuccess {
		return
	}
	a.DB.Model(&model.Project{}).Where("id = ?", projectID).
		Update("version", tasks[0].ProjectVersion)
}
