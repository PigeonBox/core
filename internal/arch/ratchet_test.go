// ratchet_test.go 架构守卫第二组：单例消费面 + 全局配置棘轮。
//
// 与 guard_test.go 的依赖边界规则互补，这两条守卫固化 2026-10-10 架构治理波
// 的成果，防回归：
//
//  7. admin 包级单例入口（Default/EffectiveUserSettings/Audit）的消费面仅限
//     装配层（bootstrap）与管理面适配层——用户面 handler 曾直连
//     admin.EffectiveUserSettings（跨面隐形依赖，import 图上不可见），已收编为
//     user.DefaultsProvider 注入；本守卫封死回归路径。
//  8. conf.GetGlobalConfig() 直读点按文件计数棘轮：基线只许减不许增
//     （新增消费面即红）。全局配置直读是环境依赖（测试须整体换全局态），
//     长期方向是构造注入收敛，棘轮先冻结存量。
package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// moduleRoot 从 go test 的 CWD（包目录）向上定位模块根。
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("向上未找到 go.mod，无法定位模块根")
		}
		root = parent
	}
}

// walkProductionGoFiles 遍历全部非测试 .go 文件（rel 为模块根相对路径，斜杠分隔）。
func walkProductionGoFiles(t *testing.T, root string, fn func(rel, abs string)) {
	t.Helper()
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "data", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		fn(filepath.ToSlash(rel), path)
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
}

// adminSingletonAllow 规则 7 白名单：目录前缀 + 精确文件（值为放行理由）。
// 只许删不许加；新增消费面必须改为装配注入（先例：user.DefaultsProvider）。
var adminSingletonAllow = map[string]string{
	"app/admin/":                             "域内部（单例宿主）",
	"bootstrap/":                             "装配层（单例的唯一合法接线点）",
	"gen/handler/admin/":                     "管理面适配层（同面消费）",
	"gen/handler/maintenance/":               "维护面适配层（管理面的一部分）",
	"gen/handler/setup/":                     "首启向导面（写 SystemConfig 属管理职责）",
	"transport/http/handler/admin_manage.go": "管理面手工 handler 区（同面消费；收口 TODO：随 IDL 化迁 gen）",
}

// adminSingletonFuncs 规则 7 盯防的包级单例入口。
var adminSingletonFuncs = map[string]bool{
	"Default":               true,
	"EffectiveUserSettings": true,
	"Audit":                 true,
}

func allowAdminSingleton(rel string) bool {
	for entry := range adminSingletonAllow {
		if strings.HasSuffix(entry, "/") {
			if strings.HasPrefix(rel, entry) {
				return true
			}
		} else if rel == entry {
			return true
		}
	}
	return false
}

// TestAdminSingletonConsumers 规则 7：import 了 app/admin 的文件里，白名单外
// 不得出现 <alias>.Default() / <alias>.EffectiveUserSettings(...) / <alias>.Audit(...)。
// AST 判定（别名无关），文本 grep 会被 adminsvc/adminapp 等别名变体绕过。
func TestAdminSingletonConsumers(t *testing.T) {
	root := moduleRoot(t)
	var violations []string

	walkProductionGoFiles(t, root, func(rel, abs string) {
		if strings.HasPrefix(rel, "app/admin/") || allowAdminSingleton(rel) {
			return
		}
		fset := token.NewFileSet()
		imp, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		aliases := map[string]bool{}
		for _, i := range imp.Imports {
			if strings.Trim(i.Path.Value, `"`) != modulePrefix+"app/admin" {
				continue
			}
			if i.Name != nil {
				aliases[i.Name.Name] = true
			} else {
				aliases["admin"] = true // 包默认名
			}
		}
		if len(aliases) == 0 {
			return
		}
		full, err := parser.ParseFile(fset, abs, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(full, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if aliases[id.Name] && adminSingletonFuncs[sel.Sel.Name] {
				violations = append(violations, fmt.Sprintf(
					"规则7 admin 单例入口越面消费: %s 调 %s.%s()（改装配注入，先例 user.DefaultsProvider）",
					rel, id.Name, sel.Sel.Name))
			}
			return true
		})
	})

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("发现 %d 处 admin 单例消费面违规:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// globalConfigBaseline 规则 8 基线：conf.GetGlobalConfig() 直读点按文件计数
// （2026-10-10 治理波后的存量）。只许减不许增；减少后请把本表同步改小。
var globalConfigBaseline = map[string]int{
	"app/admin/config.go":                      3,
	"app/admin/service.go":                     1,
	"app/admin/stats.go":                       1,
	"app/share/localfiles.go":                  2,
	"app/share/localimport.go":                 1,
	"app/share/service.go":                     1,
	"app/storage/service.go":                   1,
	"app/user/service.go":                      2,
	"bootstrap/bootstrap.go":                   1,
	"bootstrap/handlers_public.go":             1,
	"gen/handler/chunk/chunk_service.go":       1,
	"gen/handler/presign/presign_service.go":   1,
	"gen/handler/setup/setup_service.go":       1,
	"gen/handler/share/share_service.go":       3,
	"gen/handler/storage/storage_service.go":   1,
	"pkg/gate/gate.go":                         4,
	"pkg/gate/quota.go":                        1,
	"pkg/middleware/apikey.go":                 2,
	"pkg/middleware/lockout.go":                1,
	"pkg/middleware/ratelimit.go":              1,
	"pkg/security/ssrf.go":                     2,
	"pkg/utils/expire.go":                      2,
	"pkg/utils/filecheck.go":                   6,
	"transport/http/handler/oidc.go":           1,
	"transport/http/handler/settings_admin.go": 1,
}

// TestGlobalConfigRatchet 规则 8：conf.GetGlobalConfig() 直读点棘轮。
// conf/ 包自身（定义处与包内回退）不计。计数含注释中的字面出现——口径与
// 基线生成一致，改注释也可能触发，属有意的保守。
func TestGlobalConfigRatchet(t *testing.T) {
	root := moduleRoot(t)
	var violations []string

	walkProductionGoFiles(t, root, func(rel, abs string) {
		if strings.HasPrefix(rel, "conf/") || strings.HasPrefix(rel, "internal/arch/") {
			return
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		n := strings.Count(string(content), "conf.GetGlobalConfig(")
		if n == 0 {
			return
		}
		base, known := globalConfigBaseline[rel]
		switch {
		case !known:
			violations = append(violations, fmt.Sprintf(
				"规则8 新增 conf.GetGlobalConfig() 直读面: %s（%d 处）——新代码请构造注入，确需直读须在基线登记并说明", rel, n))
		case n > base:
			violations = append(violations, fmt.Sprintf(
				"规则8 conf.GetGlobalConfig() 直读点增加: %s %d → %d（棘轮只许减不许增）", rel, base, n))
		}
	})

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("发现 %d 处全局配置棘轮违规:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}
