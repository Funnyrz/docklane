# 安全验证记录

> 本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。

当前源码版本为 `0.3.2-beta.2`。以下扫描与测试记录仅描述已执行的检查，不代表 Beta 已完成充分、严格测试或适用于生产环境。历史版本证据仍按原日期和范围保留。

## 已采用的边界

- 一次初始化、Argon2id 密码、散列会话、CSRF、同源 WebSocket 和会话撤销。
- SQLite 敏感值 AES-GCM 加密；主密钥独立存放并核对数据库指纹。
- 请求异常处理不输出 Cookie、请求体或 panic 值；错误携带请求追踪 ID。
- 路径白名单、符号链接检查、备份源/仓库递归拒绝；Docker Socket 不作为宿主机文件访问授权。
- 长任务恢复、原容器保留、写入者确认、受限临时备份/恢复容器；无任意 Shell API。
- 原位置恢复失败或中断后保留写入者停止状态，要求验证数据后显式启动，不能把配置回退当作数据回滚。

## 依赖扫描与补丁

扫描发现并更新：Vue 3.5.42（包含 server-renderer 修补）、golang.org/x/net 0.60.0、quic-go 0.59.1。Vue 为纯 SPA，未使用服务端渲染，但仍更新到修补版本。

`pnpm --dir web audit --prod` 在补丁后报告无已知漏洞。此结论仅覆盖扫描时的生产 npm 依赖，不代表完整安全认证。

Go govulncheck v1.9.0 补丁后仍报告 Docker 单体 SDK 模块的两项告警：GO-2026-4883、GO-2026-4887。不能将该扫描标记为零告警。上游将这两项界定为 Docker **daemon** 插件权限与 AuthZ 插件问题，修复版本为 Engine 29.3.1；旧 github.com/docker/docker 模块在 Go 漏洞库中标记为所有版本受影响。

本程序链接官方客户端 SDK，不承载 Docker daemon 或插件授权实现，也不提供插件安装接口；据此判断这两条不是本程序中的 daemon 代码执行路径。实际部署宿主机仍需维护安全的 Docker Engine，尤其不要在未修补 Engine 上依赖插件授权边界。当前真实集成环境 Engine 29.5.2 高于上述修复版本；“支持测试 Docker 28”不等于为旧 Engine 提供安全背书。

依据：[上游插件权限公告](https://github.com/moby/moby/security/advisories/GHSA-pxq6-2prw-chj9)、[上游 AuthZ 公告](https://github.com/moby/moby/security/advisories/GHSA-x744-4wpc-v9h2)、[Go 漏洞记录](https://pkg.go.dev/vuln/GO-2026-4887)。后续升级到分拆的 SDK 模块时需重新核对这些告警，而不是简单忽略扫描退出码。

## 不应推断的结论

静态检查、单元测试和局部 Linux 集成测试不能替代完整渗透测试、所有发行版权限验证或所有故障条件验证。Docker Socket 本身具有高权限；临时执行容器虽无 privileged、无网络、无宿主机根目录挂载，仍按需授予明确的数据读取或元数据恢复能力。
