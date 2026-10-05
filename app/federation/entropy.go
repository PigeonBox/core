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

// estimateEntropyBits 粗粒度熵估计：min(len, 唯一字符数) × log2(字符集池)。
// 字符集池：数字 10 / 小写 26 / 大写 26 / 其他符号 33。取唯一字符数下界——
// 重复字符不增熵（2026-10-05 审计修正），宁可错杀——低熵码本就不该出站。
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
	// 唯一字符数下界（2026-10-05 审计 P3）：重复字符不增熵，"aaaa…a"（32 位）
	// 此前按长度估 190 bits 通过 40-bit 门槛实际秒猜。取 min(长度, 唯一字符数)
	// × log2(pool)。
	set := make(map[rune]struct{}, len(code))
	for _, r := range code {
		set[r] = struct{}{}
	}
	n := float64(len([]rune(code)))
	bits := math.Min(n, float64(len(set))) * math.Log2(float64(pool))
	return int(bits)
}

// announceAllowed 熵门槛判定。
func announceAllowed(code string, minBits int) bool {
	return estimateEntropyBits(code) >= minBits
}
