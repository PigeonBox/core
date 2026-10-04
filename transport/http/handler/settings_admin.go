// 管理端设置测试端点：SMTP 测试发信 / OIDC discovery 验证（AdminMiddleware 组内）。
// 读取的是叠加后的全局 conf（管理端保存的在线设置已热应用），测的就是"当前生效值"。
package handler

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	notifyApp "github.com/filescodebox/core/app/notify"
	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/pkg/resp"
)

type adminTestSMTPReq struct {
	To string `json:"to"`
}

// AdminTestSMTP POST /admin/notify/smtp/test {"to":"you@example.com"}
// 用当前生效的 notify.smtp 配置向指定邮箱发送测试邮件。
func AdminTestSMTP(ctx context.Context, c *app.RequestContext) {
	var req adminTestSMTPReq
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.To) == "" {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "需要收件邮箱 to"})
		return
	}
	smtp := conf.GetGlobalConfig().Notify.SMTP
	if smtp.Host == "" {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "SMTP 未配置（host 为空）"})
		return
	}
	mailer := notifyApp.NewSMTPMailer(smtp.Host, smtp.Port, smtp.Username, smtp.Password, smtp.From)
	if err := mailer.SendTo(strings.TrimSpace(req.To), "FilesCodeBox SMTP 测试邮件",
		"这是一封来自 FilesCodeBox 的测试邮件，收到即代表 SMTP 配置生效。"); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "发送失败: " + err.Error()})
		return
	}
	resp.SuccessWithMessage(c, "测试邮件已发送", nil)
}

// AdminTestOIDC POST /admin/oidc/test
// 验证当前生效 OIDC 段的 issuer discovery 端点可达且合法。
func AdminTestOIDC(ctx context.Context, c *app.RequestContext) {
	svc := getOIDCService()
	if svc == nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "OIDC 未配置"})
		return
	}
	if err := svc.TestDiscovery(ctx); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}
	resp.SuccessWithMessage(c, "OIDC discovery 验证通过", nil)
}
