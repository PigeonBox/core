package presign

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/filescodebox/core/storage"
)

// fakeObjectStore 真预签名直传能力的测试替身
type fakeObjectStore struct {
	putURL  string
	putErr  error
	headObj map[string]int64 // objectKey → 实际大小
	headErr error
}

func (f *fakeObjectStore) PresignPutURL(_ context.Context, objectKey string, _ time.Duration) (string, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	return f.putURL + objectKey, nil
}

func (f *fakeObjectStore) HeadObject(_ context.Context, objectKey string) (int64, string, error) {
	if f.headErr != nil {
		return 0, "", f.headErr
	}
	if sz, ok := f.headObj[objectKey]; ok {
		return sz, "etag-1", nil
	}
	return 0, "", errors.New("object not found")
}

func TestInit_S3PresignMode(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetObjectStore(&fakeObjectStore{putURL: "https://s3.example/bucket/"})
	ctx := context.Background()

	res, err := svc.Init(ctx, InitMeta{
		FileName:    "a.bin",
		FileSize:    1024,
		ContentType: "application/x-foo",
		ExpireValue: 1,
		ExpireStyle: "hour",
	})
	require.NoError(t, err)
	assert.Equal(t, SchemeS3, res.Scheme)
	assert.True(t, strings.HasPrefix(res.UploadURL, "https://s3.example/bucket/uploads/"))
	// s3 模式：直传 URL 是预签名地址，不再携带自家中转 token header
	assert.Equal(t, "application/x-foo", res.Headers["Content-Type"])
	assert.NotContains(t, res.Headers, "X-Upload-Token")
	// token 仍下发（Complete 凭证），且 meta 已按 s3 模式持久化
	assert.NotEmpty(t, res.Token)
}

func TestInit_FallsBackToSelfWhenUnsupported(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetObjectStore(&fakeObjectStore{putErr: storage.ErrPresignUnsupported})
	ctx := context.Background()

	res, err := svc.Init(ctx, InitMeta{FileName: "a.txt", FileSize: 10, ContentType: "text/plain"})
	require.NoError(t, err)
	assert.Equal(t, SchemeSelf, res.Scheme)
	assert.Contains(t, res.UploadURL, "/api/v1/presign/upload-direct/")
	assert.Equal(t, res.Token, res.Headers["X-Upload-Token"])
}

func TestInit_NilObjectStoreKeepsSelfMode(t *testing.T) {
	svc, _ := newTestService(t)
	res, err := svc.Init(context.Background(), InitMeta{FileName: "a.txt", FileSize: 10})
	require.NoError(t, err)
	assert.Equal(t, SchemeSelf, res.Scheme)
}

func TestComplete_S3UsesActualObjectSize(t *testing.T) {
	svc, _ := newTestService(t)
	mock := &mockShareService{returnCode: "s3code"}
	svc.SetShareService(mock)
	store := &fakeObjectStore{putURL: "https://s3.example/b/", headObj: map[string]int64{}}
	svc.SetObjectStore(store)

	ctx := context.Background()
	initRes, err := svc.Init(ctx, InitMeta{FileName: "big.bin", FileSize: 100, ContentType: "application/x-foo"})
	require.NoError(t, err)

	// 模拟客户端已直传到 S3：实际 80 字节（与声明 100 不符 → 以 S3 为事实源）
	store.headObj[initRes.ObjectKey] = 80
	completed, err := svc.Complete(ctx, initRes.UploadID, initRes.Token, "1.2.3.4")
	require.NoError(t, err)
	assert.EqualValues(t, 80, completed.FileSize)
	assert.Equal(t, SchemeS3, completed.Scheme)
	require.NotNil(t, mock.lastReq)
	assert.EqualValues(t, 80, mock.lastReq.Size)
}

func TestComplete_S3ObjectMissingNotMarkedComplete(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetShareService(&mockShareService{returnCode: "s3code"})
	store := &fakeObjectStore{putURL: "https://s3.example/b/", headObj: map[string]int64{}}
	svc.SetObjectStore(store)

	ctx := context.Background()
	initRes, err := svc.Init(ctx, InitMeta{FileName: "big.bin", FileSize: 100, ContentType: "application/x-foo"})
	require.NoError(t, err)

	// 对象尚未上传 → Complete 报错，且不标记 complete（可重传后重试）
	_, err = svc.Complete(ctx, initRes.UploadID, initRes.Token, "1.2.3.4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "对象尚未上传")

	// 客户端补传后重试 → 成功
	store.headObj[initRes.ObjectKey] = 100
	completed, err := svc.Complete(ctx, initRes.UploadID, initRes.Token, "1.2.3.4")
	require.NoError(t, err)
	assert.Equal(t, "s3code", completed.ShareCode)
}
