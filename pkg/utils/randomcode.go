// randomcode.go 随机码/令牌与上传路径的统一生成收口。
//
// 此前 crypto/rand 循环在 share/anonymous/presign/user/request 各写一份
// （含各自"取熵失败回退时间戳取模"分支），上传相对路径 uploads/YYYY/MM/DD/
// 也散落 4 处——本文件收口为单一实现。
package utils

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// RandomString 从 charset 等概率取 n 个字符（crypto/rand）。
// 取熵失败时以时间戳取模兜底（crypto/rand 几乎不会失败，与历史实现同语义）。
func RandomString(charset string, n int) string {
	max := big.NewInt(int64(len(charset)))
	b := make([]byte, n)
	for i := range b {
		e, err := rand.Int(rand.Reader, max)
		if err != nil {
			e = big.NewInt(time.Now().UnixNano() % int64(len(charset)))
		}
		b[i] = charset[e.Int64()]
	}
	return string(b)
}

// RandomHex nBytes 字节随机数转 hex（产出 2*nBytes 位字符串）。
func RandomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewUploadRelPath 上传相对路径 uploads/YYYY/MM/DD/<uuid><ext>。
// 单次取时钟构造日期段（避免跨秒边界日期漂移）；返回 (唯一文件名, 相对路径)。
func NewUploadRelPath(originalName string) (uuidName, rel string) {
	ext := filepath.Ext(originalName)
	uuidName = uuid.New().String() + ext
	now := time.Now()
	rel = filepath.Join("uploads", now.Format("2006"), now.Format("01"), now.Format("02"), uuidName)
	return uuidName, rel
}
