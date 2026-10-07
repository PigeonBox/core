package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pigeonbox/core/storage/opendal"
	"github.com/stretchr/testify/require"
)

// fakeDriver 记录 Driver 调用并按 key 存数据（并发安全：MergeChunks 会异步清理）。
type fakeDriver struct {
	mu    sync.Mutex
	data  map[string][]byte
	calls []string
}

func newFakeDriver() *fakeDriver { return &fakeDriver{data: map[string][]byte{}} }

// get/len 带锁读取：测试断言侧禁止裸读 f.data——MergeChunks 会异步触发
// CleanChunks goroutine，裸读与其持锁删除构成数据竞争（-race 偶发红）。
func (f *fakeDriver) get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[key]
	return append([]byte(nil), d...), ok
}

func (f *fakeDriver) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data)
}

func (f *fakeDriver) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeDriver) hasCall(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeDriver) Write(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Write:" + key)
	f.data[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeDriver) WriteStream(_ context.Context, key string, r io.Reader, size int64) error {
	// 先在锁外读完（chain reader 会回调 Reader/Stat 再抢同一把锁，Mutex 不可重入）
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("size mismatch: got %d want %d", len(data), size)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("WriteStream:%s:%d", key, size))
	f.data[key] = data
	return nil
}

func (f *fakeDriver) Read(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Read:" + key)
	if d, ok := f.data[key]; ok {
		return append([]byte(nil), d...), nil
	}
	return nil, fmt.Errorf("%w: %s", os.ErrNotExist, key)
}

func (f *fakeDriver) Reader(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Reader:" + key)
	if d, ok := f.data[key]; ok {
		return io.NopCloser(bytes.NewReader(d)), nil
	}
	return nil, fmt.Errorf("%w: %s", os.ErrNotExist, key)
}

func (f *fakeDriver) Stat(_ context.Context, key string) (*opendal.Metadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Stat:" + key)
	if d, ok := f.data[key]; ok {
		return &opendal.Metadata{Path: key, Size: int64(len(d))}, nil
	}
	return nil, fmt.Errorf("%w: %s", os.ErrNotExist, key)
}

func (f *fakeDriver) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Delete:" + key)
	delete(f.data, key)
	return nil
}

func (f *fakeDriver) RemoveAll(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("RemoveAll:" + key)
	prefix := key + "/"
	for k := range f.data {
		if k == key || strings.HasPrefix(k, prefix) {
			delete(f.data, k)
		}
	}
	return nil
}

// Presign 让 fakeDriver 具备 presigner 能力（验证 Operator→驱动的预签名分派）
func (f *fakeDriver) Presign(_ context.Context, method, key string, _ time.Duration) (*opendal.PresignedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Presign:" + method + ":" + key)
	return &opendal.PresignedResult{
		URL:    "https://signed.example/" + method + "/" + key,
		Method: method,
	}, nil
}

// newRemoteTestService 构造走 fake 驱动的 s3 服务
func newRemoteTestService(fd *fakeDriver) *StorageService {
	return &StorageService{
		config: &StorageConfig{Type: StorageTypeS3, DataPath: "unused-remote", BaseURL: "http://x"},
		op:     opendal.NewCustom(opendal.SchemeS3, fd),
	}
}

// newTestFileHeader 构造内存 multipart 文件头
func newTestFileHeader(t *testing.T, name string, content []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	req := httptest.NewRequest("POST", "/", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(int64(len(content)+1024)))
	fh, err := cFileHeader(req)
	require.NoError(t, err)
	return fh
}

func cFileHeader(req *http.Request) (*multipart.FileHeader, error) {
	f, h, err := req.FormFile("file")
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return h, nil
}

func TestRemoteDispatchSaveFile(t *testing.T) {
	fd := newFakeDriver()
	s := newRemoteTestService(fd)
	content := []byte("hello pigeonbox s3 dispatch")

	fh := newTestFileHeader(t, "a.bin", content)
	res, err := s.SaveFile(context.Background(), fh, "uploads/2026/10/03/a.bin")
	require.NoError(t, err)
	require.True(t, res.Success)
	require.EqualValues(t, len(content), res.FileSize)

	sum := sha256.Sum256(content)
	require.Equal(t, hex.EncodeToString(sum[:]), res.FileHash)

	stored, ok := fd.get("uploads/2026/10/03/a.bin")
	require.True(t, ok)
	require.Equal(t, content, stored)
}

func TestRemoteChunkFlow(t *testing.T) {
	fd := newFakeDriver()
	s := newRemoteTestService(fd)
	ctx := context.Background()

	parts := [][]byte{[]byte("AAA"), []byte("BBB"), []byte("CCC")}
	for i, p := range parts {
		require.NoError(t, s.SaveChunk(ctx, "u1", i, p))
	}
	require.Equal(t, 3, fd.size())

	require.NoError(t, s.MergeChunks(ctx, "u1", 3, "uploads/m.bin"))
	merged, ok := fd.get("uploads/m.bin")
	require.True(t, ok)
	require.Equal(t, []byte("AAABBBCCC"), merged)
	require.True(t, fd.hasCall("WriteStream:uploads/m.bin:9"))

	// CleanChunks（异步 goroutine + 显式调用都安全：fakeDriver 并发安全）
	require.NoError(t, s.CleanChunks(ctx, "u1"))
	_, ok2 := fd.get("chunks/u1/chunk_0")
	require.False(t, ok2)
}

func TestRemoteDownload(t *testing.T) {
	fd := newFakeDriver()
	s := newRemoteTestService(fd)
	ctx := context.Background()
	require.NoError(t, s.SaveBytes(ctx, "uploads/x.txt", []byte("xyz")))

	require.True(t, s.FileExists(ctx, "uploads/x.txt"))
	size, err := s.GetFileSize(ctx, "uploads/x.txt")
	require.NoError(t, err)
	require.EqualValues(t, 3, size)

	rc, n, err := s.GetFileReader(ctx, "uploads/x.txt")
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, "xyz", string(got))

	data, err := s.GetFile(ctx, "uploads/x.txt")
	require.NoError(t, err)
	require.Equal(t, "xyz", string(data))

	require.NoError(t, s.DeleteFile(ctx, "uploads/x.txt"))
	require.False(t, s.FileExists(ctx, "uploads/x.txt"))
}

func TestSaveBytesGuard(t *testing.T) {
	// local：路径逃逸必须被拒
	s := NewStorageService(&StorageConfig{Type: StorageTypeLocal, DataPath: t.TempDir()})
	require.Error(t, s.SaveBytes(context.Background(), "../evil.txt", []byte("x")))
	require.Error(t, s.SaveBytes(context.Background(), ".", []byte("x")))
	require.NoError(t, s.SaveBytes(context.Background(), "uploads/ok.txt", []byte("ok")))
	got, err := os.ReadFile(filepath.Join(s.dataPath(), "uploads/ok.txt"))
	require.NoError(t, err)
	require.Equal(t, "ok", string(got))

	// remote：正常写入路由到驱动
	fd := newFakeDriver()
	rs := newRemoteTestService(fd)
	require.NoError(t, rs.SaveBytes(context.Background(), "uploads/r.bin", []byte("abc")))
	require.True(t, fd.hasCall("Write:uploads/r.bin"))
}

func TestReloadFailureKeepsOld(t *testing.T) {
	fd := newFakeDriver()
	s := newRemoteTestService(fd)
	ctx := context.Background()
	require.NoError(t, s.SaveBytes(ctx, "a.txt", []byte("1")))

	// 缺参的 s3 配置 → Reload 失败且保留原后端
	err := s.Reload(&StorageConfig{Type: StorageTypeS3})
	require.Error(t, err)
	require.Equal(t, StorageTypeS3, s.EffectiveType())
	require.True(t, s.FileExists(ctx, "a.txt"))

	// 合法切换（local）
	require.NoError(t, s.Reload(&StorageConfig{Type: StorageTypeLocal, DataPath: t.TempDir()}))
	require.Equal(t, StorageTypeLocal, s.EffectiveType())
	require.NoError(t, s.InitError())
}

func TestNewStorageServiceFallback(t *testing.T) {
	// E 版：远端配置非法直接报错
	_, err := NewStorageServiceE(&StorageConfig{Type: StorageTypeS3})
	require.Error(t, err)

	// 兼容版：降级 local 且原因可查
	s := NewStorageService(&StorageConfig{Type: StorageTypeS3, DataPath: t.TempDir()})
	require.Equal(t, StorageTypeLocal, s.EffectiveType())
	require.Error(t, s.InitError())

	// 合法 webdav 配置正常点亮
	s2, err := NewStorageServiceE(&StorageConfig{Type: StorageTypeWebDAV, WebDAVURL: "http://127.0.0.1:0/dav"})
	require.NoError(t, err)
	require.Equal(t, StorageTypeWebDAV, s2.EffectiveType())
}

func TestConfigFromConfRoundTrip(t *testing.T) {
	// conf → StorageConfig → conf 双向映射不丢字段
	orig := &StorageConfig{
		Type: StorageTypeS3, DataPath: "/data", BaseURL: "http://h:1",
		Endpoint: "http://e:9000", AccessKey: "ak", SecretKey: "sk",
		Bucket: "b", Region: "r", UseSSL: true, PathStyle: true,
	}
	conf := orig.ToConf()
	back := ConfigFromConf(conf, orig.BaseURL)
	require.Equal(t, orig, back)
}

func TestPresignURLRouting(t *testing.T) {
	fd := newFakeDriver()
	s := newRemoteTestService(fd)

	u, err := s.PresignPutURL(context.Background(), "uploads/a.bin", time.Minute)
	require.NoError(t, err)
	require.Contains(t, u, "/PUT/uploads/a.bin")

	u, err = s.PresignGetURL(context.Background(), "uploads/a.bin", time.Minute)
	require.NoError(t, err)
	require.Contains(t, u, "/GET/uploads/a.bin")

	// local 不支持真预签名 → ErrPresignUnsupported（presign 域据此回退自家中转）
	local := NewStorageService(&StorageConfig{Type: StorageTypeLocal, DataPath: t.TempDir()})
	_, err = local.PresignPutURL(context.Background(), "x", time.Minute)
	require.ErrorIs(t, err, ErrPresignUnsupported)
	_, err = local.PresignGetURL(context.Background(), "x", time.Minute)
	require.ErrorIs(t, err, ErrPresignUnsupported)
}

func TestHeadObject(t *testing.T) {
	fd := newFakeDriver()
	s := newRemoteTestService(fd)
	require.NoError(t, s.SaveBytes(context.Background(), "uploads/h.bin", []byte("12345")))
	size, _, err := s.HeadObject(context.Background(), "uploads/h.bin")
	require.NoError(t, err)
	require.EqualValues(t, 5, size)
	_, _, err = s.HeadObject(context.Background(), "uploads/nope")
	require.Error(t, err)

	local := NewStorageService(&StorageConfig{Type: StorageTypeLocal, DataPath: t.TempDir()})
	require.NoError(t, local.SaveBytes(context.Background(), "a.txt", []byte("xy")))
	size, _, err = local.HeadObject(context.Background(), "a.txt")
	require.NoError(t, err)
	require.EqualValues(t, 2, size)
}
