// WebDAV 中文/特殊文件名回归（上游踩坑 vastsa #319）：
// SaveFile → GetFileReader → FileExists → DeleteFile 完整链路，
// 文件名经 URL 转义后内容不损坏。
package storage

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"golang.org/x/net/webdav"
)

func newWebDAVStorage(t *testing.T) *StorageService {
	t.Helper()
	srv := httptest.NewServer(&webdav.Handler{FileSystem: webdav.Dir(t.TempDir()), LockSystem: webdav.NewMemLS()})
	t.Cleanup(srv.Close)
	return NewStorageService(&StorageConfig{
		Type:           StorageTypeWebDAV,
		DataPath:       t.TempDir(),
		WebDAVURL:      srv.URL,
		WebDAVUsername: "u",
		WebDAVPassword: "p",
	})
}

func TestWebDAV_CJKFilenameRoundtrip(t *testing.T) {
	st := newWebDAVStorage(t)
	ctx := context.Background()

	cases := []struct{ name, content string }{
		{"中文文件名-报告终稿.txt", "内容-中文正文"},
		{"日本語ファイル.pdf", "pdf-bytes"},
		{"file with spaces & symbols +.dat", "dat-content"},
		{"混合mixed名.docx", "docx-content"},
	}
	for _, tc := range cases {
		rel := "uploads/2026/10/03/" + tc.name

		res, err := st.SaveFile(ctx, makeFileHeader(t, tc.name, []byte(tc.content)), rel)
		if err != nil {
			t.Fatalf("SaveFile %q 失败: %v", tc.name, err)
		}
		if res.FileHash == "" {
			t.Fatalf("SaveFile %q 未产出哈希", tc.name)
		}

		rc, size, err := st.GetFileReader(ctx, rel)
		if err != nil {
			t.Fatalf("GetFileReader %q 失败: %v", tc.name, err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(got) != tc.content {
			t.Fatalf("GET %q 内容不一致: %q != %q", tc.name, string(got), tc.content)
		}
		if size != int64(len(tc.content)) {
			t.Fatalf("GET %q size=%d 期望 %d", tc.name, size, len(tc.content))
		}
		if !st.FileExists(ctx, rel) {
			t.Fatalf("FileExists %q = false", tc.name)
		}
		if err := st.DeleteFile(ctx, rel); err != nil {
			t.Fatalf("DeleteFile %q 失败: %v", tc.name, err)
		}
		if st.FileExists(ctx, rel) {
			t.Fatalf("删除后 %q 仍存在", tc.name)
		}
	}
}
