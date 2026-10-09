// shareblocked.go 分享管控拒绝态错误（治理状态机的 typed error 词汇表）。
//
// 历史：app/share.ShareBlockedError 与 app/anonymous.BlockedError 曾各持一份
// 逐字相同的实现（消息文案与 errcode 映射同款，状态常量还一个用 model 常量
// 一个用字面量，已有漂移），2026-10-10 合并至此；两域以类型别名消费，
// handler 侧 errors.As / resp.NewTypedError 用法不变。
package errors

import "github.com/pigeonbox/contracts/errcode"

// 分享治理状态字面量。pkg 层不得 import repo（架构守卫规则 1），故此处独立
// 定义并与 repo/db/model 的同名常量对齐——app 层测试钉死防漂移断言。
const (
	ShareStatusBlocked       = "blocked"
	ShareStatusPendingReview = "pending_review"
)

// ShareBlocked 分享处于管控拒绝态（管理员禁用 / 待审核）。
// handler 侧按 ErrCode 透传（20012 blocked / 20013 pending_review）。
type ShareBlocked struct {
	Status string
}

func (e *ShareBlocked) Error() string {
	if e.Status == ShareStatusPendingReview {
		return "分享内容待审核，暂不可取件"
	}
	return "分享已被管理员禁用"
}

func (e *ShareBlocked) ErrCode() int {
	if e.Status == ShareStatusPendingReview {
		return errcode.CodeSharePendingReview
	}
	return errcode.CodeShareBlocked
}
