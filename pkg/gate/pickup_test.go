package gate

import (
	"context"
	"testing"

	"github.com/pigeonbox/core/conf"
	"github.com/pigeonbox/core/pkg/middleware"
	"github.com/pigeonbox/core/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withLockout 设定锁定阈值并重建全局锁定器（内存模式，表随重建清空），
// 保证用例间失败计数互不串扰。
func withLockout(t *testing.T, maxAttempts int) {
	t.Helper()
	cfg := &conf.AppConfiguration{}
	cfg.Security.Lockout = conf.LockoutConfig{
		Enabled: true, MaxAttempts: maxAttempts, WindowSeconds: 300, LockSeconds: 600,
	}
	old := conf.GetGlobalConfig()
	conf.SetGlobalConfig(cfg)
	middleware.InitDefaultLockout(nil)
	t.Cleanup(func() {
		conf.SetGlobalConfig(old)
		middleware.InitDefaultLockout(nil)
	})
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := utils.HashPassword(pw)
	require.NoError(t, err)
	return h
}

func pickup(st PickupState, req PickupCheck) PickupDecision {
	return CheckPickup(context.Background(), st, req)
}

// TestCheckPickup_VerdictOrder 判定顺序固定：过期 → 管控 → 就绪 → 密码。
func TestCheckPickup_VerdictOrder(t *testing.T) {
	withLockout(t, 10)
	base := PickupCheck{ClientIP: "1.2.3.4", Code: "ABC123", Password: ""}

	d := pickup(PickupState{Expired: true, Blocked: true, NotReady: true, RequireAuth: true}, base)
	assert.Equal(t, PickupUnavailable, d.Verdict, "过期优先于一切")

	d = pickup(PickupState{Blocked: true, BlockedStatus: "pending_review", NotReady: true, RequireAuth: true}, base)
	assert.Equal(t, PickupBlocked, d.Verdict)
	assert.Equal(t, "pending_review", d.BlockedStatus)

	d = pickup(PickupState{NotReady: true, RequireAuth: true}, base)
	assert.Equal(t, PickupNotReady, d.Verdict)
}

// TestCheckPickup_NoAuthAndTokenBypass 无密码分享直接放行；持下载令牌跳过密码段。
func TestCheckPickup_NoAuthAndTokenBypass(t *testing.T) {
	withLockout(t, 10)
	d := pickup(PickupState{}, PickupCheck{ClientIP: "1.2.3.4", Code: "X"})
	assert.Equal(t, PickupAllowed, d.Verdict)

	d = pickup(PickupState{RequireAuth: true, PasswordHash: mustHash(t, "pw")},
		PickupCheck{ClientIP: "1.2.3.4", Code: "X", AuthedByToken: true})
	assert.Equal(t, PickupAllowed, d.Verdict, "令牌等价已认证，空密码也放行")
}

// TestCheckPickup_MissingPasswordSemantics 空密码：MissingCountsFailure=false
// 是提示态（不记账），=true 按失败记账（对齐 preview/anonymous/download 历史规格）。
func TestCheckPickup_MissingPasswordSemantics(t *testing.T) {
	withLockout(t, 10)
	st := PickupState{RequireAuth: true, PasswordHash: mustHash(t, "pw")}

	d := pickup(st, PickupCheck{ClientIP: "1.2.3.4", Code: "M1", Password: ""})
	assert.Equal(t, PickupPasswordMissing, d.Verdict)

	d = pickup(st, PickupCheck{ClientIP: "1.2.3.4", Code: "M2", Password: "", MissingCountsFailure: true})
	assert.Equal(t, PickupPasswordWrong, d.Verdict)
}

// TestCheckPickup_WrongRightPassword 密码错误记账、正确放行并清零计数。
func TestCheckPickup_WrongRightPassword(t *testing.T) {
	withLockout(t, 10)
	st := PickupState{RequireAuth: true, PasswordHash: mustHash(t, "right")}
	req := PickupCheck{ClientIP: "1.2.3.4", Code: "PW1", Password: "wrong"}

	assert.Equal(t, PickupPasswordWrong, pickup(st, req).Verdict)

	req.Password = "right"
	assert.Equal(t, PickupAllowed, pickup(st, req).Verdict)

	// 脏数据：require_auth=true 而哈希缺失 → 按密码错误拒绝（不得放行）
	dirty := PickupState{RequireAuth: true, PasswordHash: ""}
	req.Password = "anything"
	assert.Equal(t, PickupPasswordWrong, pickup(dirty, req).Verdict)
}

// TestCheckPickup_LockoutSharedAcrossEndpoints 达阈值触发锁定；锁定判定先于
// 密码校验（正确密码也拒）；scope 统一使"换入口"不再重置爆破预算。
func TestCheckPickup_LockoutSharedAcrossEndpoints(t *testing.T) {
	withLockout(t, 2)
	st := PickupState{RequireAuth: true, PasswordHash: mustHash(t, "right")}
	wrong := PickupCheck{ClientIP: "9.9.9.9", Code: "LOCK1", Password: "bad"}

	assert.Equal(t, PickupPasswordWrong, pickup(st, wrong).Verdict)
	assert.Equal(t, PickupPasswordWrong, pickup(st, wrong).Verdict) // 达到阈值 2

	d := pickup(st, wrong)
	assert.Equal(t, PickupLocked, d.Verdict, "第三次应先撞锁定")
	assert.Greater(t, d.LockRemain, 0)

	right := PickupCheck{ClientIP: "9.9.9.9", Code: "LOCK1", Password: "right"}
	assert.Equal(t, PickupLocked, pickup(st, right).Verdict, "锁定期内正确密码也拒")

	// 不同 (IP, code) 维度互不影响
	other := PickupCheck{ClientIP: "9.9.9.8", Code: "LOCK1", Password: "right"}
	assert.Equal(t, PickupAllowed, pickup(st, other).Verdict)
}

// TestPickupLockKeyHelpers 枚举记账与锁定预检走同一键。
func TestPickupLockKeyHelpers(t *testing.T) {
	withLockout(t, 2)
	ctx := context.Background()

	RecordPickupFailure(ctx, "5.5.5.5", "ENUM1")
	RecordPickupFailure(ctx, "5.5.5.5", "ENUM1")
	remain, locked := PickupLockedRemain(ctx, "5.5.5.5", "ENUM1")
	assert.True(t, locked, "两次枚举记账后应锁定")
	assert.Greater(t, remain, 0)

	_, locked = PickupLockedRemain(ctx, "5.5.5.5", "ENUM2")
	assert.False(t, locked, "其他码不受影响")
}
