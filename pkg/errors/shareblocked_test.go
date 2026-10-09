package errors

import (
	"testing"

	"github.com/pigeonbox/contracts/errcode"
	"github.com/stretchr/testify/assert"
)

func TestShareBlocked_Mapping(t *testing.T) {
	pending := &ShareBlocked{Status: ShareStatusPendingReview}
	assert.Equal(t, "分享内容待审核，暂不可取件", pending.Error())
	assert.Equal(t, errcode.CodeSharePendingReview, pending.ErrCode())

	blocked := &ShareBlocked{Status: ShareStatusBlocked}
	assert.Equal(t, "分享已被管理员禁用", blocked.Error())
	assert.Equal(t, errcode.CodeShareBlocked, blocked.ErrCode())

	// 未知状态按禁用兜底（与合并前两域行为一致）
	unknown := &ShareBlocked{Status: "weird"}
	assert.Equal(t, "分享已被管理员禁用", unknown.Error())
	assert.Equal(t, errcode.CodeShareBlocked, unknown.ErrCode())
}
