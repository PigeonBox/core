package storage

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declaredStorageTypes AST 扫描本包全部非测试源文件，收集 StorageType 常量
// 声明值。兼容两种声明形态：
//
//	StorageTypeLocal  StorageType = "local"          （storage.go）
//	StorageTypeOSS    = StorageType("oss")           （providers.go）
//
// 用源码扫描而非硬编码清单：新增类型常量而忘记注册/入清单时守卫依然生效。
func declaredStorageTypes(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err, f)
		for _, decl := range af.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if !strings.HasPrefix(vs.Names[0].Name, "StorageType") {
					continue
				}
				switch v := vs.Values[0].(type) {
				case *ast.BasicLit: // StorageType = "..."
					seen[strings.Trim(v.Value, `"`)] = true
				case *ast.CallExpr: // = StorageType("...")
					if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "StorageType" {
						if lit, ok := v.Args[0].(*ast.BasicLit); ok {
							seen[strings.Trim(lit.Value, `"`)] = true
						}
					}
				}
			}
		}
	}
	require.NotEmpty(t, seen, "扫描不到任何 StorageType 常量，守卫已失效")
	return seen
}

// TestBackendRegistryCoversAllDeclaredTypes 每个声明的 StorageType 必须
// 「已注册远端构建器」或「显式 local」二选一——第三个状态（未注册被静默当
// local）正是 2026-10-05 探测事故的根源，注册表化后由本守卫封死。
func TestBackendRegistryCoversAllDeclaredTypes(t *testing.T) {
	for typ := range declaredStorageTypes(t) {
		if isLocalBackend(StorageType(typ)) {
			assert.False(t, isRemoteBackend(StorageType(typ)), "local 不应注册远端构建器: %s", typ)
			continue
		}
		assert.True(t, isRemoteBackend(StorageType(typ)),
			"StorageType %q 未注册构建器：新后端须在 backends.go backendBuilders 登记（漏注册=ProbeConfig 拒收+启动报错）", typ)
	}
}

// TestBuildOperatorDispatch 分派语义：local/空 → (nil,nil)；未知 → fail-loud；
// 已注册但配置不完整 → 构建器的校验错误（不是静默 local）。
func TestBuildOperatorDispatch(t *testing.T) {
	for _, typ := range []StorageType{"", StorageTypeLocal} {
		op, err := buildOperator(&StorageConfig{Type: typ})
		assert.NoError(t, err)
		assert.Nil(t, op, "local 语义 = nil operator: %q", typ)
	}

	_, err := buildOperator(&StorageConfig{Type: StorageType("s33")})
	require.Error(t, err, "未知类型必须 fail-loud，不得静默当 local")
	assert.Contains(t, err.Error(), "未知存储类型")

	_, err = buildOperator(&StorageConfig{Type: StorageTypeS3})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3 配置不完整")

	_, err = buildOperator(&StorageConfig{Type: StorageTypeCOS, Region: "ap-guangzhou"})
	require.Error(t, err, "云厂商缺 bucket/凭据须报 ResolveCloudProvider 校验错误")
}

// TestProbeConfigUnknownTypeRejected 未知类型不得再走 local 探测放行（旧
// default 分支的事故面）。
func TestProbeConfigUnknownTypeRejected(t *testing.T) {
	err := ProbeConfig(context.Background(), &StorageConfig{
		Type:     StorageType("mystery"),
		DataPath: t.TempDir(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未知存储类型")
}

// TestProbeConfigLocal local/空类型仍走路径可写探测（行为与旧 default 分支一致）。
func TestProbeConfigLocal(t *testing.T) {
	dir := t.TempDir()
	for _, typ := range []StorageType{"", StorageTypeLocal} {
		assert.NoError(t, ProbeConfig(context.Background(), &StorageConfig{Type: typ, DataPath: dir}))
	}
	err := ProbeConfig(context.Background(), &StorageConfig{Type: StorageTypeLocal})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "存储路径未配置")
}
