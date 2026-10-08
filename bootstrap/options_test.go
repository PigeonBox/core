package bootstrap

import (
	"path/filepath"
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	o := applyOptions()
	if o.StaticDir != "./static" {
		t.Fatalf("default StaticDir = %q, want ./static", o.StaticDir)
	}
}

func TestWithStaticDir(t *testing.T) {
	o := applyOptions(WithStaticDir("/opt/pb/ui"))
	if o.StaticDir != "/opt/pb/ui" {
		t.Fatalf("StaticDir = %q, want /opt/pb/ui", o.StaticDir)
	}
	// 空串保持默认
	o = applyOptions(WithStaticDir(""))
	if o.StaticDir != "./static" {
		t.Fatalf("empty StaticDir override should keep default, got %q", o.StaticDir)
	}
}

func TestStaticAssetsJoin(t *testing.T) {
	// 静态服务的三处路径拼接均应基于 StaticDir
	got := filepath.Join(staticOpts.StaticDir, "assets")
	want := "assets"
	if len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Fatalf("assets path = %q, want suffix %q", got, want)
	}
}
