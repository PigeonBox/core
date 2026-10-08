package middleware

import "testing"

// TestMetricsBoundPath 路径标签基数封顶（2026-10-08 加固）：distinct 超上限后
// 新路径必须并入 "_overflow"——归一化启发式防不住全小写随机串，硬上限兜底。
func TestMetricsBoundPath(t *testing.T) {
	m := &Metrics{seenPaths: map[string]struct{}{}, pathBounded: true}

	if got := m.boundPath("/api/config"); got != "/api/config" {
		t.Fatalf("首见路径应原样记录, got %q", got)
	}
	if got := m.boundPath("/api/config"); got != "/api/config" {
		t.Fatalf("已见路径应原样放行, got %q", got)
	}

	// 填满剩余配额（首条已占 1）
	for i := 0; len(m.seenPaths) < maxPathLabelCardinality; i++ {
		m.boundPath(string(rune('a'+i%26)) + "/seg" + string(rune('a'+i/26%26)) + itoa(i))
	}
	if len(m.seenPaths) != maxPathLabelCardinality {
		t.Fatalf("配额应被打满, got %d", len(m.seenPaths))
	}

	if got := m.boundPath("/totally/brand/new/path"); got != overflowPathLabel {
		t.Fatalf("超限新路径必须并入 %q, got %q", overflowPathLabel, got)
	}
	// 已见路径不受超限影响
	if got := m.boundPath("/api/config"); got != "/api/config" {
		t.Fatalf("超限后已见路径仍应原样放行, got %q", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestNormalizePathDynamicSegments 归一化回归：动态段仍收敛
func TestNormalizePathDynamicSegments(t *testing.T) {
	cases := map[string]string{
		"/share/metadata/AbCd1234Ef":                                "/share/metadata/:id",
		"/anonymous/download/12345678":                              "/anonymous/download/:id",
		"/chunk/upload/status/550e8400-e29b-41d4-a716-446655440000": "/chunk/upload/status/:id",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}
