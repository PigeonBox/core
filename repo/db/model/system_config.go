package model

import "gorm.io/gorm"

// SystemConfigRecord 系统配置持久化记录（管理后台在线修改的站点配置）。
// 单行存储（id=1），Data 为 admin.SystemConfig 的 JSON 序列化——不走通用 KV，
// 也不回写 config.yaml（容器/K8s 场景 config 文件可能只读，DB 是所有部署
// 形态下唯一必可写存储）。
type SystemConfigRecord struct {
	gorm.Model
	Data string `gorm:"type:text" json:"data"`
}

// TableName 单表语义：固定表名。
func (SystemConfigRecord) TableName() string { return "system_configs" }
