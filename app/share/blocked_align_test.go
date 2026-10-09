package share

import (
	"testing"

	pkgerrors "github.com/pigeonbox/core/pkg/errors"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
)

// TestShareBlockedStatusAlignedWithModel pkg/errors.ShareBlocked 的状态字面量
// 必须与 repo/db/model 的治理状态常量一致——pkg 层不得 import repo（架构守卫
// 规则 1），常量在两侧各自定义，本断言钉死防漂移（漂移=管控拒绝态误映射
// errcode，取件面把待审核当禁用回给客户端）。
func TestShareBlockedStatusAlignedWithModel(t *testing.T) {
	assert.Equal(t, model.StatusPendingReview, pkgerrors.ShareStatusPendingReview)
	assert.Equal(t, model.StatusBlocked, pkgerrors.ShareStatusBlocked)
}
