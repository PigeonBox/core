# pkg/ - core 横切设施

此目录存放 core 内部的横切工具和通用组件。

## 目录结构

```
pkg/
├── auth/        # 认证（JWT/API Key）
├── errors/      # 错误处理（错误码定义在 contracts errcode）
├── gate/        # 功能门控
├── logger/      # 日志工具封装
├── metrics/     # 指标
├── middleware/  # HTTP 中间件
├── resp/        # HTTP 响应工具
├── security/    # 安全设施（SSRF 校验等）
├── transfer/    # 传输相关
└── utils/       # 通用工具函数
```

## 与 kit 的边界

| 位置 | 用途 | 定位 |
|------|------|------|
| [`filescodebox/kit`](https://github.com/filescodebox/kit) | 通用工具库（async/retry/ratelimit/… 共 28 包） | 零生态依赖，任何项目可 `go get` 消费 |
| 本 `pkg/` | core 横切设施 | 与 core 业务/配置绑定的专用设施，随 core 版本走 |

## 主要组件

### errors/
- AppError 错误结构
- errcode（定义在 contracts）到错误消息/HTTP 响应的映射

### logger/
- Zap 日志封装
- 日志级别、文件输出配置
- 便捷日志方法

### resp/
- HTTP 响应工具函数
- Success/Error/Page 等标准响应
- 错误码自动映射

## 添加新工具

1. 在 `pkg/` 下创建新目录
2. 实现工具函数
3. 在需要的地方导入使用

```go
import "github.com/filescodebox/core/pkg/yourpkg"
```
