// local.go 本地后端路径语义：逃逸收口（localContain）与历史双层布局解析（resolveLocal/LocalAbsPath）。
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// localContain 本地后端路径收口：rel 清洗后必须仍落在 DataPath 根内，返回绝对路径。
// 2026-10-05 审计修复：此前仅写侧（SaveStream/SaveBytes）有逃逸守卫，读/删/Stat
// 侧裸 Join——DB 中 RelPath 被投毒（如 multi-bind 的 object_key）即可任意读/删/
// Stat 服务器文件。所有本地路径访问统一经此。
func (s *StorageService) localContain(rel string) (string, error) {
	root := filepath.Clean(s.dataPath())
	full := filepath.Clean(filepath.Join(root, rel))
	r, err := filepath.Rel(root, full)
	if err != nil || full == root || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("illegal storage path: %s", rel)
	}
	return full, nil
}

// resolveLocal 定位本地文件：优先 DataPath+rel，失败回退 DataPath+/uploads/+rel。
// 兼容统一存储实例前的历史双层布局（chunk/share 懒加载单例的 DataPath 分别为
// ./data 与 ./data/uploads，直传历史文件落在 data/uploads/uploads/<rel>）。
// 两条候选路径均经 localContain 收口，逃逸（../）一律视为不存在。
func (s *StorageService) resolveLocal(rel string) (string, bool) {
	p, err := s.localContain(rel)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	alt, err := s.localContain(filepath.Join("uploads", rel))
	if err != nil {
		return p, false
	}
	if _, err := os.Stat(alt); err == nil {
		return alt, true
	}
	return p, false
}

// LocalAbsPath 本地后端下解析存储相对路径为绝对路径（仅 local 后端；远端返回空）。
// 供下载链路走 c.File（原生 Range/断点续传/MIME 推断）；路径不存在返回空。
func (s *StorageService) LocalAbsPath(rel string) string {
	if s.EffectiveType() != StorageTypeLocal {
		return ""
	}
	p, _ := s.resolveLocal(rel)
	if p == "" {
		return ""
	}
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return ""
	}
	return p
}
