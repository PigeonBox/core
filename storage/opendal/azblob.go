package opendal

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// azblobDriver Azure Blob 存储驱动（手写 REST，零 SDK 依赖）。
// 认证：共享密钥（HMAC-SHA256 签名）或 SAS token（二选一，SAS 优先）。
// endpoint 缺省 https://<account>.blob.core.windows.net（Azurite/主权云可覆盖）。
// key 拼接在 container 之下，root 为容器内前缀（可为空）。
type azblobDriver struct {
	account   string
	container string
	key       string
	sas       string
	endpoint  string
	root      string
	client    *http.Client
}

// newAzBlobDriver 构造 Azure Blob 驱动。
// options: account/container/key/sas/endpoint/root
func newAzBlobDriver(opts map[string]string) (*azblobDriver, error) {
	account := strings.TrimSpace(opts["account"])
	container := strings.TrimSpace(opts["container"])
	if account == "" || container == "" {
		return nil, errors.New("azblob: account/container 必填")
	}
	if strings.TrimSpace(opts["key"]) == "" && strings.TrimSpace(opts["sas"]) == "" {
		return nil, errors.New("azblob: key（共享密钥）或 sas 至少配置一项")
	}
	endpoint := strings.TrimSpace(opts["endpoint"])
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.blob.core.windows.net", account)
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasPrefix(endpoint, "http") {
		return nil, fmt.Errorf("azblob: endpoint 需为 http(s) 地址，得 %q", endpoint)
	}
	return &azblobDriver{
		account:   account,
		container: container,
		key:       opts["key"],
		sas:       strings.TrimSpace(opts["sas"]),
		endpoint:  endpoint,
		root:      strings.Trim(opts["root"], "/"),
		client:    &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

// blobURL key → 完整 blob URL（含 SAS，若有）
func (d *azblobDriver) blobURL(key string) string {
	u := fmt.Sprintf("%s/%s/%s", d.endpoint, d.container, blobJoin(d.root, key))
	if d.sas != "" {
		if !strings.HasPrefix(d.sas, "?") {
			u += "?"
		}
		u += d.sas
	}
	return u
}

// containerURL 容器级操作 URL（List），含 SAS
func (d *azblobDriver) containerURL(query string) string {
	u := fmt.Sprintf("%s/%s", d.endpoint, d.container)
	if d.sas != "" {
		if !strings.HasPrefix(d.sas, "?") {
			u += "?"
		}
		u += d.sas
		u += "&"
	}
	return u + query
}

func blobJoin(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, "/")
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "/")
}

// sign 组装 SharedKey 授权头（SAS 模式返回空）。
// 签名串规范：https://learn.microsoft.com/rest/api/storageservices/authorization-for-the-azure-storage-services
func (d *azblobDriver) sign(req *http.Request, contentLength int64) {
	if d.sas != "" || d.key == "" {
		return
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("x-ms-date", date)
	req.Header.Set("x-ms-version", "2021-08-06")

	// canonicalized x-ms-* 头（按名称排序）
	var xms []string
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-ms-") {
			xms = append(xms, lk+":"+strings.Join(vs, ","))
		}
	}
	sort.Strings(xms)
	canonicalHeaders := strings.Join(xms, "\n")

	// canonicalized resource：/account/container/blob + 排序的子资源查询参数
	u := req.URL
	cr := "/" + d.account + u.Path
	if u.RawQuery != "" {
		var pairs []string
		for _, kv := range strings.Split(u.RawQuery, "&") {
			if kv != "" {
				pairs = append(pairs, kv)
			}
		}
		sort.Strings(pairs)
		for _, kv := range pairs {
			cr += "\n" + kv
		}
	}

	cl := ""
	if contentLength > 0 {
		cl = strconv.FormatInt(contentLength, 10)
	}
	ct := req.Header.Get("Content-Type")
	rangeHdr := req.Header.Get("Range")
	stringToSign := strings.Join([]string{
		req.Method,
		"", // content-encoding
		"", // content-language
		cl, // content-length
		"", // content-md5
		ct,
		"", // date（用 x-ms-date）
		"", // if-modified-since
		"", // if-match
		"", // if-none-match
		"", // if-unmodified-since
		rangeHdr,
	}, "\n")
	if canonicalHeaders != "" {
		stringToSign += "\n" + canonicalHeaders
	}
	stringToSign += "\n" + cr

	keyBytes, derr := base64.StdEncoding.DecodeString(d.key)
	if derr != nil {
		keyBytes = []byte(d.key) // 配错时签名必然被服务端拒绝，此处不中断
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", "SharedKey "+d.account+":"+sig)
}

func (d *azblobDriver) Write(ctx context.Context, key string, data []byte) error {
	return d.WriteStream(ctx, key, bytes.NewReader(data), int64(len(data)))
}

func (d *azblobDriver) WriteStream(ctx context.Context, key string, r io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.blobURL(key), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	d.sign(req, size)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return fmt.Errorf("azblob put %s: %d %s", key, resp.StatusCode, string(b))
	}
	return nil
}

func (d *azblobDriver) Read(ctx context.Context, key string) ([]byte, error) {
	rc, err := d.Reader(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

func (d *azblobDriver) Reader(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.blobURL(key), nil)
	if err != nil {
		return nil, err
	}
	d.sign(req, 0)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: azblob %s", os.ErrNotExist, key)
	}
	if resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("azblob get %s: %d", key, resp.StatusCode)
	}
	return resp.Body, nil
}

func (d *azblobDriver) Stat(ctx context.Context, key string) (*Metadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, d.blobURL(key), nil)
	if err != nil {
		return nil, err
	}
	d.sign(req, 0)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: azblob %s", os.ErrNotExist, key)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("azblob head %s: %d", key, resp.StatusCode)
	}
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	mt, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	return &Metadata{Path: key, Size: size, ModTime: mt, ETag: etag}, nil
}

func (d *azblobDriver) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, d.blobURL(key), nil)
	if err != nil {
		return err
	}
	d.sign(req, 0)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: azblob %s", os.ErrNotExist, key)
	}
	// 202 Accepted = 软删除/异步删除也算成功
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("azblob delete %s: %d", key, resp.StatusCode)
	}
	return nil
}

func (d *azblobDriver) RemoveAll(ctx context.Context, key string) error {
	if strings.Trim(key, "/") == "" {
		return errors.New("azblob: refusing RemoveAll with empty key")
	}
	// 容器内前缀列举逐个删除（Blob 无目录语义）
	blobs, err := d.listAll(ctx, blobJoin(d.root, key))
	if err != nil {
		return err
	}
	for _, b := range blobs {
		if err := d.Delete(ctx, strings.TrimPrefix(b, blobJoin(d.root, "")+"/")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// listAll 列出前缀下全部 blob 名（分页翻完）
func (d *azblobDriver) listAll(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	marker := ""
	for {
		q := "restype=container&comp=list&prefix=" + prefix
		if marker != "" {
			q += "&marker=" + marker
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.containerURL(q), nil)
		if err != nil {
			return nil, err
		}
		d.sign(req, 0)
		resp, err := d.client.Do(req)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Blobs struct {
				Blob []struct {
					Name string `xml:"Name"`
				} `xml:"Blob"`
			} `xml:"Blobs"`
			NextMarker string `xml:"NextMarker"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&parsed)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, b := range parsed.Blobs.Blob {
			out = append(out, b.Name)
		}
		if parsed.NextMarker == "" {
			return out, nil
		}
		marker = parsed.NextMarker
	}
}

func (d *azblobDriver) List(ctx context.Context, key string) ([]*Metadata, error) {
	prefix := blobJoin(d.root, key)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	blobs, err := d.listAll(ctx, prefix)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]*Metadata, 0, len(blobs))
	for _, b := range blobs {
		rel := strings.TrimPrefix(b, prefix)
		seg := rel
		isDir := false
		if i := strings.Index(rel, "/"); i >= 0 {
			seg = rel[:i+1]
			isDir = true
		}
		if seen[seg] {
			continue
		}
		seen[seg] = true
		out = append(out, &Metadata{Path: seg, IsDir: isDir})
	}
	return out, nil
}

// Probe 认证级连通性验证：容器存在且可列（403 在此暴露）。
func (d *azblobDriver) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.containerURL("restype=container&comp=list&maxresults=1"), nil)
	if err != nil {
		return err
	}
	d.sign(req, 0)
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("azblob 容器不可达: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("azblob 认证失败（403）：检查 key/sas")
	}
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("azblob 容器不存在（404）")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("azblob 容器探测失败: %d", resp.StatusCode)
	}
	return nil
}
