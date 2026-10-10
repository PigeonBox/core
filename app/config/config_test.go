package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateUserSettings 校验（自 app/admin/service_test.go 随域独立迁入）
func TestValidateUserSettings(t *testing.T) {
	require.Error(t, validateUserSettings(&UserSettings{UserUploadSize: -1}))
	require.Error(t, validateUserSettings(&UserSettings{SessionExpiryHours: -5}))
	require.Error(t, validateUserSettings(&UserSettings{SessionExpiryHours: 100000}))
	require.NoError(t, validateUserSettings(&UserSettings{SessionExpiryHours: 168}))
}
