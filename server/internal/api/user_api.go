package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/auth"
	"github.com/packrun/packrun/internal/model"
)

func (a *App) listUsers(c *gin.Context) {
	var list []model.User
	if err := a.DB.Order("id asc").Find(&list).Error; err != nil {
		internal(c, "查询用户失败")
		return
	}
	ok(c, list)
}

type userReq struct {
	Username    string  `json:"username"`
	DisplayName string  `json:"displayName"`
	Password    string  `json:"password"`
	Role        string  `json:"role"`
	ServerScope string  `json:"serverScope"`
	ParentID    *uint   `json:"parentId"` // 归属主账号；空表示自身为主账号
	Status      string  `json:"status"`
	ProjectIDs  *[]uint `json:"projectIds"` // 授权项目：以项目成员关系承载子账户的可见范围
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
	u := model.User{
		Username: req.Username, DisplayName: req.DisplayName, PasswordHash: hash,
		Role: req.Role, ServerScope: req.ServerScope, ParentID: req.ParentID, Status: status,
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
	if req.ParentID != nil {
		updates["parent_id"] = *req.ParentID
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
	a.audit(c, "user.update", u.Username, "ok", "", "调整角色/范围/状态/项目授权", false)
	ok(c, u)
}

func validUserStatus(status string) bool {
	return status == "" || status == "active" || status == "disabled"
}
