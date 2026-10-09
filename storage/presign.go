// presign.go 真预签名直传/直下 URL 生成与 s3 直传核实（HeadObject）。
package storage

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/pigeonbox/core/storage/opendal"
)

// PresignPutURL 生成真预签名直传 PUT URL（客户端直传对象存储，服务器不过流量）。
// 仅 s3 等支持离线签名的后端可用；local/webdav 返回 ErrPresignUnsupported，
// 调用方（presign 域）据此回退自家中转。
func (s *StorageService) PresignPutURL(ctx context.Context, objectKey string, expire time.Duration) (string, error) {
	return s.presignURL(ctx, objectKey, "PUT", expire)
}

// PresignGetURL 生成真预签名直下 GET URL（短时效，下载 302 用）。
// disposition 可选（首项生效）：非空时作为 response-content-disposition 签入
// URL——对象存储按此回 Content-Disposition，直下 302 客户端才能拿到文件名
// （2026-10-07 修复：直下 302 曾无文件名，浏览器按对象 UUID key 命名）。
func (s *StorageService) PresignGetURL(ctx context.Context, objectKey string, expire time.Duration, disposition ...string) (string, error) {
	opts := map[string]string{}
	if len(disposition) > 0 && disposition[0] != "" {
		opts["response-content-disposition"] = disposition[0]
	}
	return s.presignURL(ctx, objectKey, "GET", expire, opts)
}

func (s *StorageService) presignURL(ctx context.Context, objectKey, method string, expire time.Duration, opts ...map[string]string) (string, error) {
	_, op := s.current()
	if op == nil || op.Scheme() != opendal.SchemeS3 {
		return "", ErrPresignUnsupported
	}
	var o map[string]string
	if len(opts) > 0 {
		o = opts[0]
	}
	res, err := op.Presign(ctx, opendal.PresignedRequest{Path: objectKey, Method: method, Expire: expire, Opts: o})
	if err != nil {
		return "", err
	}
	return res.URL, nil
}

// HeadObject 返回对象大小与 ETag（s3 直传 Complete 时的存在性/大小核实）。
func (s *StorageService) HeadObject(ctx context.Context, objectKey string) (int64, string, error) {
	_, op := s.current()
	if op == nil {
		info, err := os.Stat(filepath.Join(s.dataPath(), objectKey))
		if err != nil {
			return 0, "", err
		}
		return info.Size(), "", nil
	}
	md, err := op.Stat(ctx, objectKey)
	if err != nil {
		return 0, "", err
	}
	return md.Size, md.ETag, nil
}
