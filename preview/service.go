package preview

import (
	"fmt"
	"log"
	"os"
)

// svc 预览服务全局单例。
// 设计权衡：bootstrap 调 InitService 初始化，各处 GetService 读取。预览功能可选
// （依赖 ffmpeg），用全局单例 + nil 判断简化调用方。未来若迁移到 DI 容器可改为构造注入。
var svc *Service

// InitService 初始化预览服务
func InitService(cfg *Config) error {
	if cfg == nil {
		cfg = &Config{
			EnablePreview:    true,
			ThumbnailWidth:   300,
			ThumbnailHeight:  200,
			MaxFileSize:      50 * 1024 * 1024, // 50MB
			PreviewCachePath: "./data/previews",
			FFmpegPath:       "ffmpeg",
		}
	}

	// 确保缓存目录存在
	if err := os.MkdirAll(cfg.PreviewCachePath, 0755); err != nil {
		return fmt.Errorf("failed to create preview cache directory: %w", err)
	}

	svc = NewService(cfg)

	// 注册生成器
	svc.RegisterGenerator(PreviewTypeImage, NewImageGenerator(cfg))
	svc.RegisterGenerator(PreviewTypeVideo, NewVideoGenerator(cfg))
	svc.RegisterGenerator(PreviewTypeAudio, NewAudioGenerator(cfg))
	svc.RegisterGenerator(PreviewTypeCode, NewCodeGenerator(cfg))

	log.Println("Preview service initialized successfully")
	return nil
}

// GetService 获取预览服务实例
func GetService() *Service {
	if svc == nil {
		// 使用默认配置初始化
		_ = InitService(nil)
	}
	return svc
}

// MaxFileSize 返回预览大小上限（字节，0=不限）。调用方应在打开对象前用
// DB 记录的文件大小预判——否则任意大文件会先被全量拉到临时盘再由生成器
// 拒绝，构成磁盘耗尽攻击面（2026-10-08 加固）。
func (s *Service) MaxFileSize() int64 {
	if s == nil || s.config == nil {
		return 0
	}
	return s.config.MaxFileSize
}
