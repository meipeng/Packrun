package api

import (
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/packrun/packrun/internal/model"
)

// GormDedupStore 把告警去重状态落在本地库：重启后同一事件在窗口内仍不会重复推送。
// watchdog 单协程调用，无需加锁；主键冲突按「已记录」处理。
type GormDedupStore struct {
	DB *gorm.DB
}

// Allow 判断 key 是否允许发送：window>0 时按窗口判定并滚动 last_at；
// window<=0 表示一次性去重——记录存在即拒绝。
func (s *GormDedupStore) Allow(key string, now time.Time, window time.Duration) bool {
	var rec model.AlertDedup
	err := s.DB.Where("key = ?", key).First(&rec).Error
	switch {
	case err == nil:
		if window <= 0 || now.Sub(rec.LastAt) < window {
			return false
		}
		res := s.DB.Model(&model.AlertDedup{}).Where("key = ?", key).Update("last_at", now)
		return res.Error == nil
	case err == gorm.ErrRecordNotFound:
		create := s.DB.Create(&model.AlertDedup{Key: key, LastAt: now})
		return create.Error == nil
	default:
		// 存储异常时放行：宁可重复告警，也不吞掉关键故障
		log.Printf("[alert] 去重存储读取失败 key=%s: %v", key, err)
		return true
	}
}

// purgeAlertDedup 清理超过保留窗口的去重记录：对应事件早已远离查询窗口，
// 保留只会让表无限增长。返回清理条数。
func purgeAlertDedup(app *App, olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	res := app.DB.Where("last_at < ?", cutoff).Delete(&model.AlertDedup{})
	if res.Error != nil {
		log.Printf("[alert] 清理过期去重记录失败: %v", res.Error)
		return 0
	}
	return int(res.RowsAffected)
}
