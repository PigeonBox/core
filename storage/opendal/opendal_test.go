package opendal

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/webdav"
)

// httptestWebDAV 在进程内临时目录起真实 WebDAV 服务
func httptestWebDAV(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(&webdav.Handler{
		FileSystem: webdav.Dir(t.TempDir()),
		LockSystem: webdav.NewMemLS(),
	})
	t.Cleanup(srv.Close)
	return srv
}

// TestWebDAVDriverRoundtrip 用 x/net/webdav 在进程内起真实 WebDAV 服务，
// 验证驱动全操作（无外部依赖）。
func TestWebDAVDriverRoundtrip(t *testing.T) {
	srv := httptestWebDAV(t)
	d, err := newWebDAVDriver(map[string]string{"url": srv.URL, "root": "box"})
	require.NoError(t, err)
	ctx := context.Background()

	// Write（自动建父目录）
	require.NoError(t, d.Write(ctx, "uploads/2026/a.txt", []byte("hello")))
	got, err := d.Read(ctx, "uploads/2026/a.txt")
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))

	// Stat
	md, err := d.Stat(ctx, "uploads/2026/a.txt")
	require.NoError(t, err)
	require.EqualValues(t, 5, md.Size)
	require.False(t, md.IsDir)

	// Reader
	rc, err := d.Reader(ctx, "uploads/2026/a.txt")
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, "hello", string(b))

	// WriteStream
	require.NoError(t, d.WriteStream(ctx, "uploads/2026/b.bin", bytes.NewReader([]byte("0123456789")), 10))
	md, err = d.Stat(ctx, "uploads/2026/b.bin")
	require.NoError(t, err)
	require.EqualValues(t, 10, md.Size)

	// 不存在 → os.ErrNotExist 语义
	_, err = d.Stat(ctx, "uploads/2026/nope")
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = d.Read(ctx, "uploads/2026/nope")
	require.ErrorIs(t, err, os.ErrNotExist)

	// List
	items, err := d.List(ctx, "uploads/2026")
	require.NoError(t, err)
	require.Len(t, items, 2)

	// MkdirAll / RemoveAll（递归）
	require.NoError(t, d.MkdirAll(ctx, "chunks/u1"))
	require.NoError(t, d.RemoveAll(ctx, "uploads"))
	_, err = d.Stat(ctx, "uploads/2026/a.txt")
	require.ErrorIs(t, err, os.ErrNotExist)

	// 空前缀防御
	require.Error(t, d.RemoveAll(ctx, ""))
}

// TestNewWebDAVDriverValidation 构造参数校验。
func TestNewWebDAVDriverValidation(t *testing.T) {
	_, err := newWebDAVDriver(map[string]string{})
	require.Error(t, err)
	_, err = newWebDAVDriver(map[string]string{"url": "ftp://x"})
	require.Error(t, err)
	_, err = newWebDAVDriver(map[string]string{"url": "http://127.0.0.1:0/dav"})
	require.NoError(t, err)
}

func TestS3DriverValidation(t *testing.T) {
	// 缺 endpoint / 缺凭据 / 缺 bucket 均报错（不再静默兜底 fs）
	_, err := newS3Driver(map[string]string{})
	require.Error(t, err)
	_, err = newS3Driver(map[string]string{"endpoint": "localhost:9000", "access_key": "a", "secret_key": "b"})
	require.Error(t, err)
	_, err = newS3Driver(map[string]string{"endpoint": "localhost:9000", "bucket": "box"})
	require.Error(t, err)

	d, err := newS3Driver(map[string]string{
		"endpoint": "https://play.min.io", "access_key": "a", "secret_key": "b",
		"bucket": "box", "region": "us-east-1",
	})
	require.NoError(t, err)
	require.Equal(t, "box", d.bucket)

	// 空前缀 RemoveAll 防御
	require.Error(t, d.RemoveAll(context.Background(), ""))
}

// TestS3PresignOffline 预签名是离线签名（无需真实服务），验证 URL 形态与 Operator 分派。
func TestS3PresignOffline(t *testing.T) {
	d, err := newS3Driver(map[string]string{
		"endpoint": "http://localhost:9000", "access_key": "minioadmin",
		"secret_key": "minioadmin", "bucket": "box", "path_style": "true",
		// 显式 region：minio-go 预签名前的 bucket location 查询走本地缓存，保持离线
		"region": "us-east-1",
	})
	require.NoError(t, err)

	res, err := d.Presign(context.Background(), "PUT", "uploads/a.bin", 30*time.Minute, nil)
	require.NoError(t, err)
	require.Equal(t, "PUT", res.Method)
	require.Contains(t, res.URL, "localhost:9000/box/uploads/a.bin")
	require.Contains(t, res.URL, "X-Amz-Signature")

	resGet, err := d.Presign(context.Background(), "GET", "uploads/a.bin", time.Minute, map[string]string{"response-content-disposition": `attachment; filename="a.bin"`})
	require.NoError(t, err)
	require.Equal(t, "GET", resGet.Method)

	_, err = d.Presign(context.Background(), "POST", "uploads/a.bin", time.Minute, nil)
	require.Error(t, err)

	// Operator 层：s3 scheme 走驱动预签名
	op := NewCustom(SchemeS3, d)
	res2, err := op.Presign(context.Background(), PresignedRequest{Path: "/uploads/a.bin", Method: "PUT", Expire: time.Minute})
	require.NoError(t, err)
	require.Contains(t, res2.URL, "X-Amz-Signature")
}
