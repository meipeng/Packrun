package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/packrun/packrun/internal/model"
)

func (a *App) listAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}

	q := a.DB.Model(&model.AuditLog{})
	if v := c.Query("action"); v != "" {
		q = q.Where("action LIKE ?", v+"%")
	}
	if v := c.Query("result"); v != "" {
		q = q.Where("result = ?", v)
	}
	if v := c.Query("actor"); v != "" {
		q = q.Where("actor LIKE ?", "%"+v+"%")
	}
	if v := c.Query("keyword"); v != "" {
		like := "%" + v + "%"
		q = q.Where("target LIKE ? OR task_no LIKE ?", like, like)
	}

	var total int64
	q.Count(&total)

	var list []model.AuditLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		internal(c, "查询审计日志失败")
		return
	}
	ok(c, gin.H{"items": list, "total": total, "page": page, "pageSize": size})
}
