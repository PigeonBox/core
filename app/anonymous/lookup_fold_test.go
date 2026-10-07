package anonymous

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pigeonbox/core/conf"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 大小写折叠回源：映射未命中后 DB 直查的折叠兜底（默认开，可关）。

// setCodeFold 换全局配置的折叠开关（沿用 globalquota_test 的换配置模式）。
func setCodeFold(t *testing.T, on bool) {
	t.Helper()
	old := conf.GetGlobalConfig()
	var cfg conf.AppConfiguration
	if old != nil {
		cfg = *old
	}
	cfg.Download.CodeCaseInsensitive = &on
	conf.SetGlobalConfig(&cfg)
	t.Cleanup(func() { conf.SetGlobalConfig(old) })
}

func TestLookupShareCode_CaseFold(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	// 只写 DB（不走 GenerateCode 写 KV）：模拟用户拿 8 位分享码直查。
	// 两条不同码：回源命中会回填映射缓存，子用例间共用 miniredis，
	// 各用一条码避免缓存互染。
	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{Code: "AbCd1234"}))
	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{Code: "ZeWr5t78"}))

	t.Run("折叠开（默认）：小写输入命中", func(t *testing.T) {
		setCodeFold(t, true)
		got, err := svc.lookupShareCode(ctx, "abcd1234")
		require.NoError(t, err)
		assert.Equal(t, "AbCd1234", got)
	})
	t.Run("折叠关：小写输入不命中（精确匹配）", func(t *testing.T) {
		setCodeFold(t, false)
		_, err := svc.lookupShareCode(ctx, "zewr5t78")
		assert.ErrorIs(t, err, ErrCodeNotFound)
		got, err := svc.lookupShareCode(ctx, "ZeWr5t78")
		require.NoError(t, err)
		assert.Equal(t, "ZeWr5t78", got)
	})
	t.Run("6 位输入仍走大写归一（取件码语义不受开关影响）", func(t *testing.T) {
		setCodeFold(t, false)
		meta := CodeMeta{ShareCode: "AbCd1234", FileName: "a.txt"}
		pickup, err := svc.GenerateCode(ctx, meta, time.Now().Add(time.Hour))
		require.NoError(t, err)
		got, err := svc.lookupShareCode(ctx, strings.ToLower(pickup))
		require.NoError(t, err)
		assert.Equal(t, "AbCd1234", got)
	})
}
