package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestBusinessCounters(t *testing.T) {
	RecordUploadBytes("direct", 1024)
	RecordShareCreated("anonymous")
	RecordRejected(RejectType)
	RecordModerationHit("pending")

	assert.Equal(t, 1024.0, testutil.ToFloat64(uploadBytes.WithLabelValues("direct")))
	assert.Equal(t, 1.0, testutil.ToFloat64(shareCreated.WithLabelValues("anonymous")))
	assert.Equal(t, 1.0, testutil.ToFloat64(uploadRejected.WithLabelValues(RejectType)))
	assert.Equal(t, 1.0, testutil.ToFloat64(moderationHits.WithLabelValues("pending")))
}
