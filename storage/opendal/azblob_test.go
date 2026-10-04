package opendal

import (
	"net/http"
	"strings"
	"testing"
)

// 回归：SharedKey 签名的 StringToSign 组装（逐字符确定性）。
// 算法参考 https://learn.microsoft.com/rest/api/storageservices/authorization-for-the-azure-storage-services
func TestAzBlobSignatureStringToSign(t *testing.T) {
	d, err := newAzBlobDriver(map[string]string{
		"account":   "acct",
		"container": "c1",
		"key":       "a2V5", // base64("key")
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPut, "https://acct.blob.core.windows.net/c1/uploads/dir-a/file1.txt", nil)
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("x-ms-date", "Mon, 04 Oct 2026 00:00:00 GMT")
	req.Header.Set("x-ms-version", "2021-08-06")
	d.sign(req, 11)

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "SharedKey acct:") {
		t.Fatalf("Authorization 形态错误: %q", auth)
	}
	sig := strings.TrimPrefix(auth, "SharedKey acct:")
	if len(sig) != 44 || !strings.Contains(sig, "=") {
		t.Fatalf("签名长度/形态异常: %q (len=%d)", sig, len(sig))
	}

	// 同一请求重复签名必须一致（确定性）；内容长度参与签名（改长度签名必须变）
	req2, _ := http.NewRequest(http.MethodPut, req.URL.String(), nil)
	req2.Header = req.Header.Clone()
	d.sign(req2, 11)
	if req2.Header.Get("Authorization") != auth {
		t.Fatal("同参数签名不一致")
	}
	req3, _ := http.NewRequest(http.MethodPut, req.URL.String(), nil)
	req3.Header = req.Header.Clone()
	d.sign(req3, 12)
	if req3.Header.Get("Authorization") == auth {
		t.Fatal("Content-Length 应参与签名")
	}

	// SAS 模式不生成 Authorization 头
	d2, err := newAzBlobDriver(map[string]string{
		"account": "acct", "container": "c1", "sas": "?sig=abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	req4, _ := http.NewRequest(http.MethodPut, d2.blobURL("a.txt"), nil)
	d2.sign(req4, 0)
	if req4.Header.Get("Authorization") != "" {
		t.Fatal("SAS 模式不应有 Authorization 头")
	}
	if !strings.Contains(req4.URL.RawQuery, "sig=abc") {
		t.Fatal("SAS token 应拼在 URL 上")
	}
}

// 配置校验：缺容器/缺凭据必须报错（不静默回退）
func TestAzBlobDriverValidation(t *testing.T) {
	if _, err := newAzBlobDriver(map[string]string{"account": "a"}); err == nil {
		t.Fatal("缺 container 应报错")
	}
	if _, err := newAzBlobDriver(map[string]string{"account": "a", "container": "c"}); err == nil {
		t.Fatal("缺 key/sas 应报错")
	}
	if _, err := newAzBlobDriver(map[string]string{"account": "a", "container": "c", "sas": "?sig=1"}); err != nil {
		t.Fatalf("sas 模式应合法: %v", err)
	}
}
