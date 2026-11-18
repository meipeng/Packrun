package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/packrun/packrun/internal/model"
)

func (a *App) listScripts(c *gin.Context) {
	var list []model.Script
	if err := a.DB.Order("phase asc, id asc").Find(&list).Error; err != nil {
		internal(c, "查询脚本失败")
		return
	}
	ok(c, list)
}

type scriptReq struct {
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	Content     string `json:"content"`
	TimeoutSec  int    `json:"timeoutSec"`
	RunAs       string `json:"runAs"`
	Description string `json:"description"`
}

func (r scriptReq) validate() string {
	if strings.TrimSpace(r.Name) == "" {
		return "脚本名称不能为空"
	}
	if r.Phase != model.PhasePreCheck && r.Phase != model.PhasePostDeploy {
		return "脚本阶段只能是 precheck 或 postdeploy"
	}
	if strings.TrimSpace(r.Content) == "" {
		return "脚本内容不能为空"
	}
	if r.TimeoutSec <= 0 || r.TimeoutSec > 3600 {
		return "超时时间需在 1-3600 秒之间"
	}
	return ""
}

func (a *App) createScript(c *gin.Context) {
	var req scriptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(c, msg)
		return
	}
	s := model.Script{
		Name: req.Name, Phase: req.Phase, Content: req.Content,
		TimeoutSec: req.TimeoutSec, RunAs: req.RunAs, Description: req.Description, Version: 1,
	}
	if err := a.DB.Create(&s).Error; err != nil {
		internal(c, "创建脚本失败")
		return
	}
	a.audit(c, "script.create", s.Name, "ok", "", "新增部署脚本", false)
	ok(c, s)
}

func (a *App) updateScript(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var s model.Script
	if err := a.DB.First(&s, id).Error; err != nil {
		notFound(c, "脚本不存在")
		return
	}
	var req scriptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "参数格式错误")
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(c, msg)
		return
	}
	a.DB.Model(&s).Updates(map[string]any{
		"name": req.Name, "phase": req.Phase, "content": req.Content,
		"timeout_sec": req.TimeoutSec, "run_as": req.RunAs, "description": req.Description,
		"version": s.Version + 1,
	})
	a.audit(c, "script.update", s.Name, "ok", "", "修改部署脚本（版本递增）", false)
	ok(c, s)
}

func (a *App) deleteScript(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var s model.Script
	if err := a.DB.First(&s, id).Error; err != nil {
		notFound(c, "脚本不存在")
		return
	}
	a.DB.Delete(&s)
	a.audit(c, "script.delete", s.Name, "ok", "", "删除部署脚本", false)
	ok(c, gin.H{"deleted": true})
}
