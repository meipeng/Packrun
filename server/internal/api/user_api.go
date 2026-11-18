package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/model"
)

// nullableUint 区分「请求里没带这个字段」与「显式传 null」。
// 归属主账号需要支持清空，用 *uint 无法表达「显式置空」，会退化成「不修改」。
type nullableUint struct {
	Set   bool
	Value *uint
}

func (n *nullableUint) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var v uint
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}

func (n nullableUint) ValueOrZero() uint {
	if n.Value == nil {
		return 0
	}
	return *n.Value
}

func (a *App) listUsers(c *gin.Context) {
	var list []model.User
	if err := a.DB.Order("id asc").Find(&list).Error; err != nil {
		internal(c, "查询用户失败")
		return
	}
	ok(c, list)
}

type userReq struct {
	Username    string       `json:"username"`
	DisplayName string       `json:"displayName"`
	Password    string       `json:"password"`
	Role        string       `json:"role"`
	ServerScope string       `json:"serverScope"`
	ParentID    nullableUint `json:"parentId"` // 归属主账号；显式传 null 表示解除归属
	Status      string       `json:"status"`
	ProjectIDs  *[]uint      `json:"projectIds"` // 授权项目：以项目成员关系承载子账户的可见范围
}

// validateParent 校验归属主账号：不能指向自己、父账号必须存在且本身是主账号。
// 只允许一层归属，避免出现链式或环形引用。
func (a *App) validateParent(userID uint, parentID *uint) string {
	if parentID == nil || *parentID == 0 {
		return ""
	}
	if userID != 0 && *parentID == userID {
		return "归属主账号不能是自己"
	}
	var p model.User
	if err := a.DB.First(&p, *parentID).Error; err != nil {
		return "归属主账号不存在"
	}
	if p.ParentID != nil && *p.ParentID != 0 {
		return "归属主账号必须是主账号，不能再挂到其他账号下"
	}
	return ""
}

// normalizeProjectGrants 校验并去重项目授权列表；不存在的项目整批拒绝。
func (a *App) normalizeProjectGrants(projectIDs []uint, ownerID uint) ([]uint, string) {
	seen := make(map[uint]bool, len(projectIDs))
	ids := make([]uint, 0, len(projectIDs))
	for _, id := range projectIDs {
		if id == 0 || seen[id] {
			continue
		}
		var p model.Project
		if err := a.DB.First(&p, id).Error; err != nil {
			return nil, "项目不存在：" + strconv.Itoa(int(id))
		}
		seen[id] = true
		// 与项目设置的成员维护保持一致：项目创建者不作为成员记录。
		if p.OwnerID == ownerID {
			continue
		}
		ids = append(ids, id)
	}
	return ids, ""
}

// replaceUserProjectGrants 整体替换用户的项目授权（项目成员关系）。
func (a *App) replaceUserProjectGrants(userID uint, projectIDs []uint) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.ProjectMember{}).Error; err != nil {
			return err
		}
		if len(projectIDs) == 0 {
			return nil
		}
		members := make([]model.ProjectMember, 0, len(projectIDs))
		for _, id := range projectIDs {
			members = append(members, model.ProjectMember{ProjectID: id, UserID: userID})
		}
		return tx.Create(&members).Error
	})
}

func (a *App) createUser(c *gin.Context) {
	var req userReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		badRequest(c, "账号不能为空")
		return
	}
	if len(req.Password) < 8 {
		badRequest(c, "初始口令至少 8 位")
		return
	}
	switch req.Role {
	case model.RoleAdmin, model.RoleDeployer, model.RoleAuditor:
	default:
		badRequest(c, "角色不合法")
		return
	}
	if !validUserStatus(req.Status) {
		badRequest(c, "状态只能是 active 或 disabled")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		internal(c, "口令加密失败")
		return
	}
	if req.ServerScope == "" {
		req.ServerScope = "*"
	}
	status := req.Status
	if status == "" {
		status = "active"
	}
	var grantIDs []uint
	if req.ProjectIDs != nil {
		var msg string
		if grantIDs, msg = a.normalizeProjectGrants(*req.ProjectIDs, 0); msg != "" {
			badRequest(c, msg)
			return
		}
	}
	if msg := a.validateParent(0, req.ParentID.Value); msg != "" {
		badRequest(c, msg)
		return
	}
	u := model.User{
		Username: req.Username, DisplayName: req.DisplayName, PasswordHash: hash,
		Role: req.Role, ServerScope: req.ServerScope, Status: status,
	}
	if req.ParentID.Set && req.ParentID.ValueOrZero() != 0 {
		u.ParentID = req.ParentID.Value
	}
	if err := a.DB.Create(&u).Error; err != nil {
		fail(c, 409, CodeConflict, "账号已存在")
		return
	}
	if req.ProjectIDs != nil {
		if err := a.replaceUserProjectGrants(u.ID, grantIDs); err != nil {
			internal(c, "更新项目授权失败")
			return
		}
	}
	a.audit(c, "user.create", u.Username, "ok", "", "角色="+u.Role+" 范围="+u.ServerScope, false)
	ok(c, u)
}

func (a *App) updateUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u model.User
	if err := a.DB.First(&u, id).Error; err != nil {
		notFound(c, "用户不存在")
		return
	}
	var req userReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	switch req.Role {
	case model.RoleAdmin, model.RoleDeployer, model.RoleAuditor:
	default:
		badRequest(c, "角色不合法")
		return
	}
	if !validUserStatus(req.Status) {
		badRequest(c, "状态只能是 active 或 disabled")
		return
	}

	var grantIDs []uint
	if req.ProjectIDs != nil {
		var msg string
		if grantIDs, msg = a.normalizeProjectGrants(*req.ProjectIDs, u.ID); msg != "" {
			badRequest(c, msg)
			return
		}
	}

	newStatus := u.Status
	if req.Status != "" {
		newStatus = req.Status
	}
	// 保护最后一个启用状态的管理员，避免系统失去管理入口。
	if u.Role == model.RoleAdmin && u.Status == "active" &&
		(req.Role != model.RoleAdmin || newStatus != "active") {
		var admins int64
		a.DB.Model(&model.User{}).Where("role = ? AND status = ?", model.RoleAdmin, "active").Count(&admins)
		if admins <= 1 {
			badRequest(c, "系统必须保留至少一个启用状态的管理员")
			return
		}
	}

	updates := map[string]any{
		"display_name": req.DisplayName,
		"role":         req.Role,
		"server_scope": req.ServerScope,
	}
	if req.ParentID.Set {
		if msg := a.validateParent(u.ID, req.ParentID.Value); msg != "" {
			badRequest(c, msg)
			return
		}
		// 显式传 null（或 0）表示解除归属，写入 NULL 而不是忽略。
		if req.ParentID.ValueOrZero() == 0 {
			updates["parent_id"] = nil
		} else {
			updates["parent_id"] = *req.ParentID.Value
		}
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	passwordReset := false
	if req.Password != "" {
		if len(req.Password) < 8 {
			badRequest(c, "口令至少 8 位")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			internal(c, "口令加密失败")
			return
		}
		updates["password_hash"] = hash
		passwordReset = true
	}
	// 口令、角色、范围或状态发生变化时递增令牌版本，使旧会话立即失效。
	if passwordReset || req.Role != u.Role || req.ServerScope != u.ServerScope || newStatus != u.Status {
		updates["token_version"] = gorm.Expr("token_version + 1")
	}
	a.DB.Model(&u).Updates(updates)
	if req.ProjectIDs != nil {
		if err := a.replaceUserProjectGrants(u.ID, grantIDs); err != nil {
			internal(c, "更新项目授权失败")
			return
		}
	}
	// 回读一次，保证响应里是最新值（尤其是被置空的 parentId）。
	if err := a.DB.First(&u, u.ID).Error; err != nil {
		internal(c, "读取用户失败")
		return
	}
	a.audit(c, "user.update", u.Username, "ok", "", "调整角色/范围/状态/项目授权", false)
	ok(c, u)
}

// deleteUser 软删除账号：保留任务与审计对操作人的引用，同时让该账号的会话立即失效。
func (a *App) deleteUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	me := currentUser(c)
	var u model.User
	if err := a.DB.First(&u, id).Error; err != nil {
		notFound(c, "用户不存在")
		return
	}
	if u.ID == me.ID {
		badRequest(c, "不能删除当前登录账号")
		return
	}
	if u.Role == model.RoleAdmin && u.Status == "active" {
		var admins int64
		a.DB.Model(&model.User{}).Where("role = ? AND status = ?", model.RoleAdmin, "active").Count(&admins)
		if admins <= 1 {
			badRequest(c, "系统必须保留至少一个启用状态的管理员")
			return
		}
	}
	// 项目创建者是项目的唯一管理者，删掉会让项目没有可维护的人。
	var owned int64
	if err := a.DB.Model(&model.Project{}).Where("owner_id = ?", u.ID).Count(&owned).Error; err != nil {
		internal(c, "检查项目归属失败")
		return
	}
	if owned > 0 {
		badRequest(c, fmt.Sprintf("该用户是 %d 个项目的创建者，请先转移或删除这些项目", owned))
		return
	}
	// 执行中的任务会继续向数据库写日志，删除操作人会让现场无法追溯。
	var running int64
	if err := a.DB.Model(&model.Task{}).
		Where("operator_id = ? AND status IN ?", u.ID,
			[]string{model.TaskPending, model.TaskUploading, model.TaskVerifying, model.TaskExecuting}).
		Count(&running).Error; err != nil {
		internal(c, "检查未完成任务失败")
		return
	}
	if running > 0 {
		badRequest(c, fmt.Sprintf("该用户还有 %d 个未完成任务，请先取消或等待结束", running))
		return
	}

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", u.ID).Delete(&model.ProjectMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", u.ID).Delete(&model.RecoveryCode{}).Error; err != nil {
			return err
		}
		// 释放账号名便于重建同名账号；历史任务里存的是 operator 字符串，不受影响。
		renamed := fmt.Sprintf("%s#deleted#%d", u.Username, time.Now().Unix())
		if err := tx.Model(&u).Updates(map[string]any{
			"username":           renamed,
			"status":             "disabled",
			"parent_id":          nil,
			"otp_enabled":        false,
			"otp_secret":         "",
			"pending_otp_secret": "",
			"pending_otp_at":     nil,
			"token_version":      gorm.Expr("token_version + 1"),
		}).Error; err != nil {
			return err
		}
		return tx.Delete(&u).Error
	})
	if err != nil {
		internal(c, "删除用户失败")
		return
	}
	a.audit(c, "user.delete", u.Username, "ok", "", "删除账号并失效其会话", false)
	ok(c, gin.H{"deleted": true})
}

// resetUserOTP 由管理员清除指定账号的动态口令与恢复码，供用户重新绑定。
// 同时递增令牌版本，让该账号所有在线会话立即失效。
func (a *App) resetUserOTP(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u model.User
	if err := a.DB.First(&u, id).Error; err != nil {
		notFound(c, "用户不存在")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&u).Updates(map[string]any{
			"otp_enabled":        false,
			"otp_secret":         "",
			"pending_otp_secret": "",
			"pending_otp_at":     nil,
			"last_otp_step":      0,
			"token_version":      gorm.Expr("token_version + 1"),
		}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", u.ID).Delete(&model.RecoveryCode{}).Error
	})
	if err != nil {
		internal(c, "重置动态口令失败")
		return
	}
	a.audit(c, "user.otp.reset", u.Username, "ok", "", "管理员清除动态口令，用户需重新绑定", false)
	ok(c, gin.H{"reset": true})
}

func validUserStatus(status string) bool {
	return status == "" || status == "active" || status == "disabled"
}
