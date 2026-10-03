package utils

import (
	"testing"
	"time"

	"github.com/filescodebox/core/conf"
	"github.com/stretchr/testify/assert"
)

func TestCalculateExpireTime_Basic(t *testing.T) {
	now := time.Now()
	tp := CalculateExpireTime(2, "hour")
	assert.NotNil(t, tp)
	diff := tp.Sub(now)
	assert.Greater(t, diff, 1*time.Hour)
	assert.Less(t, diff, 3*time.Hour)
}

func TestCalculateExpireTime_Forever(t *testing.T) {
	assert.Nil(t, CalculateExpireTime(1, "forever"))
}

func TestCalculateExpireTime_CapClamp(t *testing.T) {
	t.Run("超过上限被钳制", func(t *testing.T) {
		old := conf.GetGlobalConfig()
		conf.SetGlobalConfig(&conf.AppConfiguration{Upload: conf.UploadConfig{MaxSaveSecondsCap: 3600}})
		t.Cleanup(func() { conf.SetGlobalConfig(old) })

		start := time.Now()
		tp := CalculateExpireTime(2, "hour") // 请求 2h，上限 1h
		diff := tp.Sub(start)
		assert.GreaterOrEqual(t, diff, 3599*time.Second)
		assert.LessOrEqual(t, diff, 3601*time.Second)
	})
	t.Run("未超上限不受影响", func(t *testing.T) {
		old := conf.GetGlobalConfig()
		conf.SetGlobalConfig(&conf.AppConfiguration{Upload: conf.UploadConfig{MaxSaveSecondsCap: 7200}})
		t.Cleanup(func() { conf.SetGlobalConfig(old) })

		start := time.Now()
		tp := CalculateExpireTime(1, "hour")
		diff := tp.Sub(start)
		assert.Greater(t, diff, 59*time.Minute)
		assert.Less(t, diff, 61*time.Minute)
	})
	t.Run("上限为0不限", func(t *testing.T) {
		withUploadConfig(t, conf.UploadConfig{MaxSaveSecondsCap: 0})
		start := time.Now()
		tp := CalculateExpireTime(1, "year")
		assert.Greater(t, tp.Sub(start), 300*24*time.Hour)
	})
}

func TestCalculateExpireCount(t *testing.T) {
	assert.Equal(t, 5, CalculateExpireCount("count", 5))
	assert.Equal(t, -1, CalculateExpireCount("day", 5))
}
