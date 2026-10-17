# 浅色云平台界面升级验收

验收日期：2026-10-17（Asia/Shanghai）。本目录独立保存新视觉证据，原 `web/qa/` 的工业风及发布验收记录保留为历史证据。

## 交付内容

- 冷白内容区、深蓝导航、蓝色主操作；统一字号、圆角、间距、阴影及语义状态色。
- 本地 SVG 品牌与导航图标；工作台真实状态、指标、主机配置、保护信息和最近任务的层级调整。
- 全站资源表格、表单、空态、错误、分页、代码预览、深色日志/终端；Naive UI 抽屉、弹窗和下拉菜单同步主题。
- 桌面资源操作列可见，长名称省略并提供完整名称提示；手机保留表格内部横滚。短屏导航独立滚动，退出入口可达。
- 移动导航关闭时不可进入键盘焦点；打开时聚焦当前入口并循环 Tab；Escape、遮罩和页面跳转关闭后返回菜单按钮；桌面恢复正常导航。
- 配置编辑器在根节点设置等宽字体，输入区和自动高度测量使用相同字体。

## 修改文件与简化

| 区域 | 文件 | 说明 |
|---|---|---|
| 应用框架 | `../../src/App.vue` | 导航、登录/初始化、主机位置标识、移动键盘行为 |
| 主题与样式 | `../../src/theme.ts`、`../../src/style.css`、`../../index.html` | CSS 变量与 Naive 主题同步、响应式和浏览器主题色 |
| 共享视觉 | `../../src/components/AppIcon.vue`、`../../src/components/Notice.vue` | 无依赖 SVG 与统一空状态 |
| 工作台 | `../../src/views/DashboardView.vue` | 主机、指标、保护和任务层级 |
| 业务页面 | `../../src/views/ResourcesView.vue`、`ComposeView.vue`、`BackupView.vue`、`TasksView.vue`、`SettingsView.vue` | 统一操作图标、编辑器、资源列和诊断表单标签 |

清理了旧的暖色硬编码、重复 file-button 覆盖、失效 metrics article 样式、抽屉内部类的强制 padding 覆盖。复用现有 Naive UI、共享表单/确认组件和已有业务逻辑；未新增依赖、后端/API/数据库改动。

## 自动化检查

- 修改前：9 个测试文件、20 项测试通过；类型检查/构建通过。
- 修改后：前端 lint（项目现有 vue-tsc 检查）、类型检查、20 项测试、生产构建通过。
- Go `test ./...`、`vet ./...` 通过；生成包含新前端的应用，并通过真实 HTTP 服务检查其资源加载。
- `git diff --check`、两份浏览器脚本语法检查通过。
- 正文、辅助文字、主按钮及成功/警告/错误等 8 组主要配色对比度均超过 4.5:1，见 [contrast-results.json](contrast-results.json)。该项为主要配色计算，不代表完整 WCAG 认证。

最终嵌入资源：`index-8bRJTueY.js`、`index-B7hWLt6S.css`。原有 Vite 500 kB 提示仍存在：当前主 JS 约 509 kB（gzip 约 157 kB），相较基线约 503 kB 增加约 1.3%；本次未增加依赖或扩大到代码分包重构。

## 真实应用浏览器检查

独立 Go 应用监听 `127.0.0.1:18097`，使用自有临时数据库和随机生成的一次性管理员；Docker endpoint 指向本任务自有、明确不存在的 Unix Socket，不连接真实 Docker Engine。

Chrome 390、768、1280、1440px 宽度检查全部 9 个导航页面，共 36 组；全部无文档横向溢出。另检查初始化/登录、移动菜单、Escape、导航后关闭、600px 短屏退出入口与导航滚动。真实断连隐藏统计，显示实际错误；浏览器无脚本错误。见 [browser-results.json](browser-results.json)。

代表截图：[登录](login-1440.png)、[真实断连工作台](dashboard-390.png)、[设置](settings-390.png)、[短屏导航](short-screen-navigation.png)。

## 浏览器视觉测试数据检查

为检查有内容的布局，`fixtures.cjs` 仅在浏览器中拦截资源 API 并提供明确的视觉测试数据。产品代码不包含这些数据。认证状态读取仍来自独立应用；4 次预览/模拟错误写请求全部被拦截，未向后端提交资源操作。

- 9 个页面 × 4 种宽度，共 36 组有内容布局。
- 390/1440px：容器 JSON 编辑、日志、终端、删除确认、Compose YAML 编辑、表单错误、任务筛选下拉、任务详情、策略表单、快照、未验证恢复及叠加风险确认。
- 断连/加载状态、未知值、完整资源名称提示、桌面操作列按钮边界和端口列不重叠。
- 确认按钮默认禁用、确认框未勾选、打开后焦点位于取消；未提交危险确认。
- 移动导航焦点循环/返回及桌面断点恢复；JSON/YAML 实际计算字体为等宽。

共 62 组截图检查通过，浏览器无脚本错误。见 [fixture-results.json](fixture-results.json)。

代表截图：[工作台预览（测试数据）](preview.png)、[桌面资源列表](fixture-containers-1440.png)、[移动终端](fixture-container-terminal-390.png)、[未验证快照](fixture-snapshots-390.png)、[恢复风险确认](fixture-restore-risk-confirm-390.png)、[筛选下拉](fixture-task-filter-menu-390.png)。

这些是视觉/交互验收，不能表述为本轮执行了真实容器变更、TTY 会话、备份或恢复。此前真实 Docker 发布验收位于原 `../README.md`，不由本轮截图替代。

## 复现

使用现有 Go/pnpm 工具链构建后，在独立临时目录启动应用，指定不存在的 Docker Socket，确保不接入生产 Engine。`DM_QA_PLAYWRIGHT` 指向已有 Playwright 包路径，不需要添加项目依赖。

```sh
pnpm --dir web build
go build -o /tmp/docker-manager-ui-review ./cmd/server
DM_LISTEN=127.0.0.1:18097 DM_DATA_DIR=/tmp/docker-manager-ui-review-data DM_DOCKER_ENDPOINT=unix:///tmp/docker-manager-ui-review-no-engine.sock /tmp/docker-manager-ui-review
```

另一个终端运行：

```sh
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/ui-upgrade/verify.cjs
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/ui-upgrade/fixtures.cjs
```

`verify.cjs` 默认要求全新数据库，以覆盖初始化并生成临时浏览器会话。仅重验已存在的验收会话可设置 `DM_QA_REUSE_SESSION=1`。脚本将会话写入权限为 0600 的 `/tmp/dm-ui-upgrade-browser-state.json`；验收后停止自有服务并删除会话和自有临时目录。

## 视觉复核

按照 frontend-design、refactoring-ui 与 visual-verdict 复核。没有外部参考设计，本次评价针对已确认的浅色云平台方向、层级和可读性，不是像素级参考图还原。评审结果见 [visual-verdict.json](visual-verdict.json)。

临时环境清理完成：自有服务已停止，临时数据库、主密钥、浏览器会话与测试二进制已删除。原有项目服务和 Docker 资源未修改。
