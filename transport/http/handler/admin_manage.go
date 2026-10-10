// Package handler 提供定制 HTTP handlers（不走 thrift IDL 生成）。
//
// admin_manage.go 瘦身（2026-10-10）：管理端增强 handler（用户 CRUD / 文件管理 /
// 富统计 / 传输日志 / 分享治理）已全部 IDL 化迁入 gen/handler/admin（契约在
// contracts/idl/admin.thrift，路由由 gen/router/admin 注册）。本文件只保留全站
// 存储实例注入——share 多文件（share_multi.go）与寄件码（file_request.go）的
// 上传落盘仍依赖 manageStorage，其依赖面维持不动。
package handler

import "github.com/pigeonbox/core/storage"

// manageStorage 全站存储实例（bootstrap 注入；share 多文件/寄件码上传落盘共用）。
var manageStorage storage.StorageInterface

// SetManageStorage 注入全站存储实例（bootstrap 调用）。
func SetManageStorage(st storage.StorageInterface) {
	manageStorage = st
}

// ManageStorage 取全站存储实例（供需要显式句柄的调用方；缺失返回 nil）。
func ManageStorage() storage.StorageInterface {
	return manageStorage
}
