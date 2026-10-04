package utils

import (
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// UploadObjectRelPath 上传对象的统一相对路径布局：
//
//	uploads/YYYY/MM/DD/<uuid><ext>
//
// 直传分享落盘与分片合并共用本函数——磁盘名一律 UUID，原始文件名只用于
// 扩展名推导与展示（消费方存 Text/FileName 字段），路径基变更只改这里。
func UploadObjectRelPath(fileName string) string {
	ext := filepath.Ext(fileName)
	now := time.Now()
	return filepath.Join("uploads", now.Format("2006"), now.Format("01"), now.Format("02"), uuid.New().String()+ext)
}
