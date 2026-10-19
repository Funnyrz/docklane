# 设置页功能说明验收

本轮将功能标识和受限原因改为可读的中英文说明，并提供按原因展开的解决方法。桌面与手机截图保存在本目录，历史验收目录保持不变。

## 实现范围

- `internal/api/dashboard.go`、`internal/workflows/host.go`：不可用能力增加 `reason_code` 和可选 `reason_params`，保留原有 `available`、`reason`。能力检测与实际操作共用宿主机策略，区分操作系统、部署模式、授权、路径、工具及文件权限。
- `web/src/capabilities.ts`、`web/src/i18n/capabilities.ts`：六项功能显示中文名称，依据稳定原因代码映射状态、解释与解决方法。未知代码或旧版响应显示“需要排查”，不猜测为权限不足。
- `web/src/views/SettingsView.vue`、`web/src/style.css`：原因直接显示，解决方法按需展开，原始诊断进一步收起；路径列表替代 `null` JSON。布局按内容对齐，避免左侧面板空白拉伸。
- `web/src/i18n/index.ts`、`web/src/i18n/views.ts`：接入双语目录，提供可读部署模式、路径摘要及手工操作指引。

状态分别为“可用”“缺少工具”“未配置”“未授权”“连接异常”“环境不支持”“需要排查”。共覆盖 17 个原因代码，包括 Restic、Docker CLI、Compose v2、允许目录、路径映射、daemon 授权、本机连接、配置文件及宿主机权限等。

解决方法对应实际配置项和运行环境。修改与重启仍要求后端两项能力均可用，并经过预览和既有确认流程。展开说明仅展示内容。检测每次报告首个阻塞原因，解决后重新检测可发现后续问题。

## 验证结果

- 前端：`pnpm --dir web test`，14 个文件、92 项测试通过；`pnpm --dir web lint`（类型检查）、`pnpm --dir web build` 通过。
- 后端：`go test ./...`、`go vet ./...` 通过；新增 API 和宿主机能力回归覆盖原因代码、参数、原有字段及禁止操作行为。
- 专项浏览器：`browser-results.json`，29 项通过。覆盖中英文、1440×900 / 1280×800 / 768×1024 / 390×844、17 个原因、详情展开、未知代码安全显示和确认门槛。仅一次被模拟接口截获的预览请求。
- 全页回归：`fixture-results.json`，120 项通过。覆盖中英文、五种屏幕尺寸、空数据、加载、断连、长值、错误提示、抽屉及确认框；4 次操作由模拟接口截获。
- 实际程序：`real-app-results.json`。独立 Go 程序嵌入当前前端，使用临时数据库和刻意不可达的 Docker Socket，通过真实初始化、登录、能力 API、中文设置及手工指引检查。
- 浏览器运行错误为 0；未发生真实宿主机配置修改或 Docker 重启。
- `git diff --check`、验收脚本语法检查通过。

代表截图：`settings-zh-CN-1440.png`、`settings-zh-CN-390.png`、`settings-en-1440.png`、`resolution-desktop.png`、`resolution-mobile.png`、`real-settings.png`。`visual-verdict.json` 是按用户改进目标进行的人工自评，不是像素复刻认证。

## 复现

先在端口 18098 启动前端开发服务，再使用可解析到 Playwright 的 Node 环境运行：

```sh
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/settings-capabilities/verify.cjs
DM_QA_OUTPUT_DIR="$PWD/web/qa/settings-capabilities" DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/compact-console/fixtures.cjs
```

`real-app.cjs` 仅应指向自己创建的全新临时程序实例（默认 18099），该实例须使用临时数据目录和不可达的 Docker Socket。脚本会创建随机密码的临时管理员，不能用于现有部署。

## 二进制与边界

`package-results.json` 指向本轮 Linux amd64 / arm64 包及其校验结果。包内包含程序和 LICENSE，验证 ELF 架构、前端文件逐字节嵌入、压缩包内容和 SHA-256。程序为 macOS 上交叉编译，未在 Linux 主机执行；真实页面验证使用同源 macOS 程序。

生产构建仍有现有的大于 500 kB JavaScript 分块提示，不影响本轮构建通过。源码工作区包含此前界面和侧栏修改，本轮打包一并包含这些修改。
