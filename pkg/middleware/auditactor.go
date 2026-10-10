// auditactor.go 审计操作者提取：从请求上下文取 (用户ID, 用户名, IP)。
//
// 此前该逻辑以 actorFromCtx 内嵌于 admin 域；config 域独立后两个域都要写
// 管理操作审计（admin_operation_logs 同表），提取逻辑下沉 pkg 层单源
// （AuthMiddleware 写入的身份 value 本就由本包提供）。
package middleware

import "context"

// AuditActor 从请求上下文提取审计操作者：
//   - id: 登录用户 ID（未登录为 nil）
//   - name: 用户名；定时任务等非请求场景无身份，记为 "system"
//   - ip: 可信代理解析后的客户端 IP
func AuditActor(ctx context.Context) (id *uint, name, ip string) {
	name = "system"
	if uid, ok := UserIDFromContext(ctx); ok {
		id = &uid
	}
	if n := UsernameFromContext(ctx); n != "" {
		name = n
	}
	ip = ClientIPFromContext(ctx)
	return id, name, ip
}
