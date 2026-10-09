// 我的通知 mine 三件套已于 2026-10-09 IDL 化（idl/notify.thrift
// Mine/UnreadCount/MarkRead），handler 迁至 core/gen/handler/notify/notify_service.go，
// 路由由 gen/router/notify 注册并挂 UserOrAPIKey（_apiMw 手工区）。
// 本文件仅保留 notify service 注入（notify_public 等消费方）。
package handler

import (
	notifyapp "github.com/pigeonbox/core/app/notify"
)

var notifySvc *notifyapp.Service

// SetNotifyService 注入 notify service
func SetNotifyService(s *notifyapp.Service) {
	notifySvc = s
}

func getNotifyService() *notifyapp.Service {
	if notifySvc == nil {
		notifySvc = notifyapp.NewService()
	}
	return notifySvc
}
