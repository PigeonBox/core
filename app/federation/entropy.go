package federation

import (
	"math"
	"strings"
	"unicode"
)

// defaultMinEntropyBits 联邦公告的口令熵门槛。
// 熵不足的口令（如 6 位数字取件码）只在单站内有效，绝不公告出站——
// 防止 registry 侧（或其攻陷者）对联公告空间做在线/离线枚举。
const defaultMinEntropyBits = 40

// estimateEntropyBits 粗粒度熵估计：len × log2(实际字符集池大小)。
// 字符集池：数字 10 / 小写 26 / 大写 26 / 其他符号 33。这是下界估计
// （不考虑重复字符惩罚），宁可错杀——低熵码本就不该出站。
func estimateEntropyBits(code string) int {
	if code == "" {
		return 0
	}
	var pool int
	if strings.ContainsFunc(code, unicode.IsDigit) {
		pool += 10
	}
	if strings.ContainsFunc(code, unicode.IsLower) {
		pool += 26
	}
	if strings.ContainsFunc(code, unicode.IsUpper) {
		pool += 26
	}
	if strings.ContainsFunc(code, func(r rune) bool {
		return !unicode.IsDigit(r) && !unicode.IsLetter(r)
	}) {
		pool += 33
	}
	if pool <= 1 {
		return 0
	}
	return int(float64(len([]rune(code))) * math.Log2(float64(pool)))
}

// announceAllowed 熵门槛判定。
func announceAllowed(code string, minBits int) bool {
	return estimateEntropyBits(code) >= minBits
}
