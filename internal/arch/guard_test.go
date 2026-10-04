// Package arch 固化 core 的包依赖边界（架构守卫，棘轮式）。
//
// 分层目标：
//
//	transport ──► app ──► repo(db/model+dao) ──► db
//	pkg / contracts 为最底层，任何方向不得倒挂
//
// 规则（违例即测试失败；白名单只允许删除条目，新增违规必须改代码）：
//  1. pkg/** 不 import app/bootstrap/gen/repo/storage/transport（最底层共享库）
//  2. app 域间禁止互 import，白名单只登记既有边（目标：只剩 presign→share 约定边）
//  3. app/** 不 import repo/db 根包（裸 gorm 会话绕过 DAO，禁止回归）
//  4. transport/** 直连 repo/db 仅限白名单文件（目标：清空，全部收口到 service）
//  5. repo/**、storage/** 不向上 import app/transport/gen/bootstrap
package arch

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePrefix = "github.com/filescodebox/core/"

// pkgForbidden 规则 1：pkg/ 禁止依赖的内部包。
var pkgForbidden = []string{"app", "bootstrap", "gen", "repo", "storage", "transport"}

// appCrossDomainAllow 规则 2：app/<from> → app/<to> 白名单（值为收口条件 TODO）。
var appCrossDomainAllow = map[string]map[string]string{
	"presign": {"share": "约定允许的唯一跨域边（预签名完成写分享表）"},
	"share":   {"moderation": "内容审核横切钩子（fail-closed 通道）"},
}

// transportRepoAllow 规则 4：transport 直连 repo/db 的文件白名单（值为收口 TODO）。
// 仅约束 dao 与 repo/db 根包（裸 gorm 会话）；repo/db/model 是共享类型词汇表，
// transport 读模型类型不算违规。
var transportRepoAllow = map[string]string{}

// upwardDeny 规则 5：底层包禁止依赖的上层包。
var upwardDeny = []string{"app", "bootstrap", "gen", "transport"}

func TestDependencyBoundaries(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var violations []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "gen", ".git", "bin", "data", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil // 测试文件允许为装配桥接而 import 任意层
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)

		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		fromDir := filepath.ToSlash(filepath.Dir(rel))
		for _, imp := range f.Imports {
			to := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(to, modulePrefix) {
				continue // 仅约束模块内部依赖；第三方/标准库不在此列
			}
			checkEdge(&violations, fromDir, rel, strings.TrimPrefix(to, modulePrefix))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("发现 %d 处依赖边界违规（棘轮守卫：白名单只许删不许加，新违例必须改代码）:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func checkEdge(violations *[]string, fromDir, relFile, to string) {
	switch {
	case fromDir == "pkg" || strings.HasPrefix(fromDir, "pkg/"):
		for _, p := range pkgForbidden {
			if matchPrefix(to, p) {
				violate(violations, fmt.Sprintf("规则1 pkg→上层: %s → %s", relFile, to))
			}
		}

	case fromDir == "app" || strings.HasPrefix(fromDir, "app/"):
		if to == "repo/db" {
			violate(violations, fmt.Sprintf("规则3 app 裸 gorm: %s → repo/db（下沉为 DAO 方法）", relFile))
			return
		}
		if strings.HasPrefix(to, "app/") {
			toDomain := domainOf(to)
			fromDomain := domainOf(fromDir)
			if fromDomain != toDomain {
				if _, ok := appCrossDomainAllow[fromDomain][toDomain]; !ok {
					violate(violations, fmt.Sprintf("规则2 app 跨域: %s → %s（未登记白名单）", fromDir, to))
				}
			}
		}

	case fromDir == "transport" || strings.HasPrefix(fromDir, "transport/"):
		// repo/db/model 是共享类型词汇表（transport 读模型类型不算违规）；
		// dao 直连与裸 gorm 会话（repo/db 根包）禁止
		if to == "repo/db" || strings.HasPrefix(to, "repo/db/dao") {
			if _, ok := transportRepoAllow[relFile]; !ok {
				violate(violations, fmt.Sprintf("规则4 transport 绕过 service 直连 repo: %s → %s", relFile, to))
			}
		}

	case fromDir == "repo" || strings.HasPrefix(fromDir, "repo/"):
		for _, p := range upwardDeny {
			if matchPrefix(to, p) {
				violate(violations, fmt.Sprintf("规则5 repo 向上依赖: %s → %s", relFile, to))
			}
		}

	case fromDir == "storage" || strings.HasPrefix(fromDir, "storage/"):
		// storage 允许 repo/db/model（GenerateFilePath 消费分享记录字段，TODO 解耦），
		// 其余上层依赖禁止
		for _, p := range upwardDeny {
			if matchPrefix(to, p) {
				violate(violations, fmt.Sprintf("规则5 storage 向上依赖: %s → %s", relFile, to))
			}
		}
	}
}

func violate(violations *[]string, msg string) { *violations = append(*violations, msg) }

// matchPrefix 精确包名或包前缀匹配（storage 匹配 storage 与 storage/opendal）。
func matchPrefix(to, pkg string) bool {
	return to == pkg || strings.HasPrefix(to, pkg+"/")
}

// domainOf "app/share/multifile.go" 所在目录 → "app/share" → "share"。
func domainOf(dir string) string {
	rest := strings.TrimPrefix(dir, "app/")
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
