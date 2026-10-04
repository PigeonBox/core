// Package mcp 实现 FileCodeBox 的 Model Context Protocol (MCP) server。
//
// 传输：Streamable HTTP（POST 单端点，JSON-RPC 2.0；与 MCP 规范对齐，
// Claude Desktop / 任意标准 MCP 客户端可直接接入）。legacy 文档中的裸 TCP
// 方案不符合 MCP 规范，本实现为标准重做。
//
// 认证：路由挂 AdminMiddleware（管理员 JWT），AI 客户端以
// Authorization: Bearer <admin token> 接入。
//
// 工具集（对齐 legacy docs/mcp-server-guide.md）：
//   share_text / get_share / list_shares / delete_share /
//   get_system_status / get_storage_info / list_users / cleanup_expired
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/filescodebox/core/pkg/utils"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
)

// 协议常量
const (
	protocolVersion = "2025-03-26"
	serverName      = "filecodebox"
)

// JSON-RPC 错误码（-32000 段为 server 自定义）
const (
	errParse     = -32700
	errInvalidRe = -32600
	errMethodNF  = -32601
	errInvalidPa = -32602
	errInternal  = -32603
	errToolFail  = -32000
)

// rpcRequest JSON-RPC 2.0 请求
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 2.0 响应
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError JSON-RPC 2.0 错误对象
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ===== 消费侧窄接口（bootstrap 注入域适配器，本包不直接依赖 admin/share 包）=====

// SystemStats 系统状态工具所需统计事实。
type SystemStats struct {
	TotalFiles   int64
	TotalUsers   int64
	TotalSize    int64
	TodayUploads int64
	ExpiredFiles int64
}

// StorageStatusInfo 存储状态工具所需事实。
type StorageStatusInfo struct {
	StorageType  string
	TotalSpace   int64
	UsedSpace    int64
	UsagePercent float64
	FileCount    int64
}

// UserRow 用户列表工具所需最小身份字段。
type UserRow struct {
	ID       uint
	Username string
	Email    string
	Status   string
}

// ShareFileInfo 分享子文件清单项。
type ShareFileInfo struct {
	Name string
	Size int64
}

// AdminAPI 系统维护类工具所需的 admin 域能力（bootstrap 注入全站唯一实例适配器）。
type AdminAPI interface {
	DeleteShareByID(ctx context.Context, id uint) error
	SystemStats(ctx context.Context) (*SystemStats, error)
	StorageStatus(ctx context.Context) (*StorageStatusInfo, error)
	Users(ctx context.Context, page, pageSize int) ([]UserRow, int64, error)
	CleanExpired(ctx context.Context) (count, freed int64, err error)
}

// ShareAPI 分享创建/查询工具所需的 share 域能力。
type ShareAPI interface {
	// CreateTextShare 创建文本分享，返回 (取件码, 完整链接)。
	CreateTextShare(ctx context.Context, text string, expireValue int, expireStyle string,
		requireAuth bool, passwordHash string, ownerIP, customCode string) (code, fullURL string, err error)
	// ShareFiles 取分享的子文件清单。
	ShareFiles(ctx context.Context, code string) ([]ShareFileInfo, error)
}

// Service MCP server。adminSvc 提供统计/存储/用户/清理能力，
// shareSvc 提供分享创建，storage/DAO 直查用于 get_share/list_shares 展示。
type Service struct {
	adminSvc AdminAPI
	shareSvc ShareAPI
	version  string
}

// NewService 创建 MCP service。依赖经 SetAdminService/SetShareService 注入
// （bootstrap 装配；未注入时对应工具返回明确错误而非静默降级）。
func NewService(version string) *Service {
	return &Service{version: version}
}

// SetAdminService 注入 admin 域能力适配器（统计/用户/维护）
func (s *Service) SetAdminService(svc AdminAPI) { s.adminSvc = svc }

// SetShareService 注入 share 域能力适配器（创建分享）
func (s *Service) SetShareService(svc ShareAPI) { s.shareSvc = svc }

// adminReady 管理类工具依赖检查（明确报错，避免 nil 指针）。
func (s *Service) adminReady() (string, bool) {
	if s.adminSvc == nil {
		return "管理服务未注入（SetAdminService）", true
	}
	return "", false
}

// Handle 处理一次 JSON-RPC POST。
// 返回 (httpStatus, responseBody)；通知类请求返回 (202, "")。
func (s *Service) Handle(ctx context.Context, body []byte) (int, []byte) {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return writeRPCError(nil, errParse, "parse error: invalid JSON")
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return writeRPCError(req.ID, errInvalidRe, "invalid request: jsonrpc must be \"2.0\" with a method")
	}

	// 通知（无 id）：不返回响应体（MCP 规范：202 Accepted）
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return 202, nil
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.ID)
	case "ping":
		return writeRPCResult(req.ID, map[string]any{})
	case "tools/list":
		return s.handleToolsList(req.ID)
	case "tools/call":
		return s.handleToolsCall(ctx, req.ID, req.Params)
	default:
		return writeRPCError(req.ID, errMethodNF, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// handleInitialize MCP 握手
func (s *Service) handleInitialize(id json.RawMessage) (int, []byte) {
	return writeRPCResult(id, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": s.version,
		},
	})
}

// ============ 工具定义 ============

type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func objSchema(props map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func strProp(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intProp(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func (s *Service) toolDefs() []toolDef {
	return []toolDef{
		{Name: "share_text", Description: "创建文本分享，返回取件码与分享链接", InputSchema: objSchema(map[string]any{
			"text":         strProp("要分享的文本内容（上限 222KB）"),
			"expire_value": intProp("过期数值，默认 1"),
			"expire_style": strProp("过期样式：minute/hour/day/week/month/year/forever，默认 day"),
			"password":     strProp("可选取件密码（提供即开启密码保护）"),
			"custom_code":  strProp("可选自定义取件码（3-32 位字母/数字/-/_，冲突报错）"),
		}, []string{"text"})},
		{Name: "get_share", Description: "按取件码查询分享信息（不消耗取件次数）", InputSchema: objSchema(map[string]any{
			"code": strProp("8 位分享码"),
		}, []string{"code"})},
		{Name: "list_shares", Description: "分页列出全站分享记录（可按取件码/文件名搜索）", InputSchema: objSchema(map[string]any{
			"page":      intProp("页码，默认 1"),
			"page_size": intProp("每页条数，默认 20，上限 100"),
			"keyword":   strProp("搜索关键字（取件码）"),
		}, nil)},
		{Name: "delete_share", Description: "删除指定分享（DB 记录 + 物理文件，写审计日志）", InputSchema: objSchema(map[string]any{
			"code": strProp("8 位分享码"),
		}, []string{"code"})},
		{Name: "get_system_status", Description: "系统运行状态（文件/用户/上传统计）", InputSchema: objSchema(nil, nil)},
		{Name: "get_storage_info", Description: "存储使用状态（类型/容量/文件数）", InputSchema: objSchema(nil, nil)},
		{Name: "list_users", Description: "分页列出用户", InputSchema: objSchema(map[string]any{
			"page":      intProp("页码，默认 1"),
			"page_size": intProp("每页条数，默认 20"),
		}, nil)},
		{Name: "cleanup_expired", Description: "清理全部过期分享（DB + 物理文件）", InputSchema: objSchema(nil, nil)},
	}
}

func (s *Service) handleToolsList(id json.RawMessage) (int, []byte) {
	return writeRPCResult(id, map[string]any{"tools": s.toolDefs()})
}

// ============ 工具执行 ============

// handleToolsCall 执行工具。工具内部失败返回 isError 内容块（MCP 惯例：
// 业务失败放进 result.isError，协议错误才用 JSON-RPC error）。
func (s *Service) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) (int, []byte) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return writeRPCError(id, errInvalidPa, "invalid params: "+err.Error())
	}

	text, isErr := s.execTool(ctx, p.Name, p.Arguments)
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
	if isErr {
		result["isError"] = true
	}
	return writeRPCResult(id, result)
}

// execTool 单工具执行，返回 (文本结果, 是否错误)
func (s *Service) execTool(ctx context.Context, name string, args json.RawMessage) (string, bool) {
	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "invalid arguments: " + err.Error(), true
		}
	}
	argStr := func(k string) string { v, _ := a[k].(string); return v }
	argInt := func(k string, def int) int {
		if v, ok := a[k].(float64); ok && int(v) > 0 {
			return int(v)
		}
		return def
	}

	switch name {
	case "share_text":
		text := argStr("text")
		if strings.TrimSpace(text) == "" {
			return "text 不能为空", true
		}
		if s.shareSvc == nil {
			return "share service 未注入", true
		}
		style := argStr("expire_style")
		if style == "" {
			style = "day"
		}
		var passwordHash string
		if pw := argStr("password"); pw != "" {
			hash, err := utils.HashPassword(pw)
			if err != nil {
				return "密码处理失败", true
			}
			passwordHash = hash
		}
		code, fullURL, err := s.shareSvc.CreateTextShare(ctx, text,
			argInt("expire_value", 1), style, passwordHash != "", passwordHash, "mcp", argStr("custom_code"))
		if err != nil {
			return "创建分享失败: " + err.Error(), true
		}
		return fmt.Sprintf("分享创建成功\n取件码: %s\n分享链接: %s", code, fullURL), false

	case "get_share":
		code := argStr("code")
		fc, err := s.fileRepo().GetByCode(ctx, code)
		if err != nil {
			return "分享不存在或已过期", true
		}
		expire := "永久"
		if fc.ExpiredAt != nil {
			expire = fc.ExpiredAt.Format("2006-01-02 15:04:05")
		}
		// 文件分享的 Text 存原始文件名（非空），须用 IsTextShare 判定（Text 非空且无文件路径）
		kind := "文件"
		if fc.IsTextShare() {
			kind = "文本"
		}
		out := fmt.Sprintf("分享信息\n取件码: %s\n类型: %s\n内容/文件名: %s\n大小: %d 字节\n剩余次数: %d（-1 不限）\n已用次数: %d\n过期时间: %s\n创建时间: %s",
			fc.Code, kind, displayFileName(fc), fc.Size, fc.ExpiredCount, fc.UsedCount, expire, fc.CreatedAt.Format("2006-01-02 15:04:05"))
		// P0 多文件：附子文件清单（仅文件分享且存在子表行时）
		if s.shareSvc != nil && !fc.IsTextShare() {
			if items, lerr := s.shareSvc.ShareFiles(ctx, code); lerr == nil && len(items) > 1 {
				out += fmt.Sprintf("\n文件清单（%d 个）:", len(items))
				for _, it := range items {
					out += fmt.Sprintf("\n  - %s（%d 字节）", it.Name, it.Size)
				}
			}
		}
		return out, false

	case "list_shares":
		page := argInt("page", 1)
		pageSize := argInt("page_size", 20)
		if pageSize > 100 {
			pageSize = 100
		}
		files, total, err := s.fileRepo().List(ctx, page, pageSize, argStr("keyword"))
		if err != nil {
			return "查询失败: " + err.Error(), true
		}
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 条分享（第 %d 页）\n", total, page)
		for _, f := range files {
			expire := "-"
			if f.ExpiredAt != nil {
				expire = f.ExpiredAt.Format("2006-01-02")
			}
			fmt.Fprintf(&b, "· %s | %s | %d 字节 | 用 %d 次 | 过期 %s\n",
				f.Code, displayFileName(f), f.Size, f.UsedCount, expire)
		}
		return b.String(), false

	case "delete_share":
		code := argStr("code")
		fc, err := s.fileRepo().GetByCode(ctx, code)
		if err != nil {
			return "分享不存在: " + code, true
		}
		if msg, missing := s.adminReady(); missing {
			return msg, true
		}
		if err := s.adminSvc.DeleteShareByID(ctx, fc.ID); err != nil {
			return "删除失败: " + err.Error(), true
		}
		return fmt.Sprintf("已删除分享 %s（%s）", code, displayFileName(fc)), false

	case "get_system_status":
		if msg, missing := s.adminReady(); missing {
			return msg, true
		}
		stats, err := s.adminSvc.SystemStats(ctx)
		if err != nil {
			return "查询失败: " + err.Error(), true
		}
		return fmt.Sprintf("系统状态\n版本: %s\n文件总数: %d\n用户总数: %d\n总占用: %d 字节\n今日上传: %d\n过期文件: %d",
			s.version, stats.TotalFiles, stats.TotalUsers, stats.TotalSize, stats.TodayUploads, stats.ExpiredFiles), false

	case "get_storage_info":
		if msg, missing := s.adminReady(); missing {
			return msg, true
		}
		st, err := s.adminSvc.StorageStatus(ctx)
		if err != nil {
			return "查询失败: " + err.Error(), true
		}
		return fmt.Sprintf("存储状态\n类型: %s\n总空间: %d 字节\n已用: %d 字节（%.1f%%）\n文件数: %d",
			st.StorageType, st.TotalSpace, st.UsedSpace, st.UsagePercent, st.FileCount), false

	case "list_users":
		if msg, missing := s.adminReady(); missing {
			return msg, true
		}
		users, total, err := s.adminSvc.Users(ctx, argInt("page", 1), argInt("page_size", 20))
		if err != nil {
			return "查询失败: " + err.Error(), true
		}
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 个用户\n", total)
		for _, u := range users {
			fmt.Fprintf(&b, "· #%d %s（%s）状态 %s\n", u.ID, u.Username, u.Email, u.Status)
		}
		return b.String(), false

	case "cleanup_expired":
		if msg, missing := s.adminReady(); missing {
			return msg, true
		}
		n, freed, err := s.adminSvc.CleanExpired(ctx)
		if err != nil {
			return "清理失败: " + err.Error(), true
		}
		return fmt.Sprintf("清理完成：%d 个过期分享，释放 %d 字节", n, freed), false

	default:
		return "unknown tool: " + name, true
	}
}

// fileRepo 惰性 repo（DB 未初始化时由调用错误兜底）
func (s *Service) fileRepo() *dao.FileCodeRepository { return dao.NewFileCodeRepository() }

// displayFileName 分享展示名（文本取前 20 字符，文件取原始名/回退 UUID 名）
func displayFileName(fc *model.FileCode) string {
	if fc == nil {
		return ""
	}
	if fc.Text != "" {
		name := fc.Text
		runes := []rune(name)
		if len(runes) > 20 {
			name = string(runes[:20]) + "…"
		}
		return name
	}
	if fc.UUIDFileName != "" {
		return fc.UUIDFileName
	}
	return fc.FilePath
}

// ============ JSON-RPC 输出辅助 ============

func writeRPCResult(id json.RawMessage, result any) (int, []byte) {
	raw, err := json.Marshal(result)
	if err != nil {
		return writeRPCError(id, errInternal, "marshal result failed")
	}
	out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: raw})
	return 200, out
}

func writeRPCError(id json.RawMessage, code int, msg string) (int, []byte) {
	if id == nil {
		id = json.RawMessage("null")
	}
	out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	return 200, out
}

// ToolNames 供测试/文档列出工具名
func (s *Service) ToolNames() []string {
	defs := s.toolDefs()
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}
