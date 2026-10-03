package gate

import (
	"testing"

	"github.com/filescodebox/core/conf"
	"github.com/stretchr/testify/assert"
)

func withConf(t *testing.T, cfg *conf.AppConfiguration) {
	t.Helper()
	old := conf.GetGlobalConfig()
	conf.SetGlobalConfig(cfg)
	t.Cleanup(func() { conf.SetGlobalConfig(old) })
}

func uid(v uint) *uint { return &v }

func TestCheckUploadAllowed(t *testing.T) {
	t.Run("配置未初始化放行", func(t *testing.T) {
		withConf(t, nil)
		assert.NoError(t, CheckUploadAllowed(nil))
	})
	t.Run("开关开匿名放行", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{Upload: conf.UploadConfig{OpenUpload: true}})
		assert.NoError(t, CheckUploadAllowed(nil))
	})
	t.Run("开关关拒绝匿名", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{Upload: conf.UploadConfig{OpenUpload: false}})
		err := CheckUploadAllowed(nil)
		assert.Error(t, err)
		if ge, ok := err.(*GateError); ok {
			assert.Equal(t, 10012, ge.ErrCode())
		} else {
			t.Fatalf("want *GateError, got %T", err)
		}
	})
	t.Run("开关关不影响登录用户", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{Upload: conf.UploadConfig{OpenUpload: false}})
		assert.NoError(t, CheckUploadAllowed(uid(1)))
	})
}

func TestCheckUploadLogin(t *testing.T) {
	t.Run("要求登录时拒绝匿名", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{Upload: conf.UploadConfig{RequireLogin: true}})
		err := CheckUploadLogin(nil)
		assert.Error(t, err)
		if ge, ok := err.(*GateError); ok {
			assert.Equal(t, 10002, ge.ErrCode())
		}
	})
	t.Run("登录用户放行", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{Upload: conf.UploadConfig{RequireLogin: true}})
		assert.NoError(t, CheckUploadLogin(uid(1)))
	})
	t.Run("默认不要求", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{})
		assert.NoError(t, CheckUploadLogin(nil))
	})
}

func TestCheckDownloadLogin(t *testing.T) {
	t.Run("要求登录时拒绝匿名", func(t *testing.T) {
		withConf(t, &conf.AppConfiguration{Download: conf.DownloadConfig{RequireLogin: true}})
		assert.Error(t, CheckDownloadLogin(nil))
	})
	t.Run("默认不要求", func(t *testing.T) {
		withConf(t, nil)
		assert.NoError(t, CheckDownloadLogin(nil))
	})
}
