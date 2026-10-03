// Package gate 上传/下载准入闸门。
//
// 服务端强制执行 upload.open_upload / upload.require_login / download.require_login
// （此前三者只透传给前端做 UI 裁剪，后端不校验，形同虚设）。
// 判定规则：
//   - open_upload=false：拒绝全部匿名上传（登录用户不受影响）
//   - upload.require_login=true：拒绝匿名上传（含 chunk/presign 等纯匿名通道）
//   - download.require_login=true：拒绝匿名取件/下载
package gate

import (
	"fmt"

	"github.com/filescodebox/contracts/errcode"
	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/pkg/metrics"
)

// GateError 携带业务码的闸门拒绝错误，handler 侧按 ErrCode 透传给前端。
type GateError struct {
	Code int
	Msg  string
}

func (e *GateError) Error() string { return e.Msg }

// ErrCode 实现 app 侧 typed error 约定（同 user.QuotaExceededError）。
func (e *GateError) ErrCode() int { return e.Code }

func newGateErr(code int, format string, args ...interface{}) *GateError {
	return &GateError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ErrUploadDisabled 上传已被管理员关闭（匿名通道）。
var ErrUploadDisabled = newGateErr(errcode.CodeUploadDisabled, "管理员已关闭匿名上传功能")

// ErrLoginRequired 需要登录才能执行该操作。
var ErrLoginRequired = newGateErr(errcode.CodeUnauthorized, "该操作需要登录后使用")

func uploadOpen() bool {
	cfg := conf.GetGlobalConfig()
	if cfg == nil {
		return true // 配置未初始化（测试/启动早期）保持可用
	}
	return cfg.Upload.OpenUpload
}

func uploadRequireLogin() bool {
	cfg := conf.GetGlobalConfig()
	return cfg != nil && cfg.Upload.RequireLogin
}

func downloadRequireLogin() bool {
	cfg := conf.GetGlobalConfig()
	return cfg != nil && cfg.Download.RequireLogin
}

// CheckUploadAllowed 匿名上传总开关（upload.open_upload）。
// 登录用户（userID != nil）不受该开关限制。
func CheckUploadAllowed(userID *uint) error {
	if userID == nil && !uploadOpen() {
		metrics.RecordRejected(metrics.RejectDisabled)
		return ErrUploadDisabled
	}
	return nil
}

// CheckUploadLogin 上传登录要求（upload.require_login）。
func CheckUploadLogin(userID *uint) error {
	if userID == nil && uploadRequireLogin() {
		metrics.RecordRejected(metrics.RejectDisabled)
		return ErrLoginRequired
	}
	return nil
}

// CheckDownloadLogin 取件/下载登录要求（download.require_login）。
func CheckDownloadLogin(userID *uint) error {
	if userID == nil && downloadRequireLogin() {
		return ErrLoginRequired
	}
	return nil
}
