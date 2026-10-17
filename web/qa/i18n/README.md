# 中英文国际化验收

## 改动

- `web/src/i18n/`：Vue I18n 11、语义化中英文词典、浏览器语言检测与本地偏好、响应式文案描述、日期/数字、状态及任务类型展示。
- `web/src/App.vue`、`main.ts`、`components/LanguageSelect.vue`、`style.css`：全局接入、初始化/登录/控制台语言选择、Naive UI 语言、HTML lang 和页面标题。
- `web/src/views/*.vue`、`components/{Notice,FormDialog,ConfirmDialog}.vue`、`api.ts`：完整界面文案与可访问标签，保留原始错误信息和底层诊断。
- 前端测试、`vite.config.ts`、`test-setup.ts`：显式中文测试环境与双语回归；README 补充使用和词条维护说明；`package.json`、`pnpm-lock.yaml` 锁定新增 `vue-i18n@11.4.14`。

资源标签页由中文标签判断改为稳定 ID；通知和动态字段复用统一词条描述，避免存储翻译结果导致切换后文案滞留。未修改后端 API、数据库、业务枚举、Cron 或计划时区；原始日志、终端输出、JSON、用户输入保留原文。

## 自动检查

- `make check` 通过：前端生产构建、类型检查、项目现有 lint、52 项前端测试、Go 测试与 race 检查、gofmt、Go vet、Shell/Python 静态检查及验收文档结构检查。
- `git diff --check` 和两个浏览器脚本语法检查通过。
- 测试覆盖语言变体、优先级、存储失效、词条键/插值参数一致性、全部双语词条编译、日期时区、API 错误原文保留、运行中切换、表单/终端/确认状态保留和请求字段不变。

## 浏览器验证

[浏览器结果](browser-results.json)：Chrome 中运行真实 Vue 界面，API 与流使用仅在浏览器中注入的测试数据，未执行 Docker 操作。简体中文和英语分别覆盖 390px、1440px：

- 初始化、登录、全部九个导航页面；额外检查手动选择优先、刷新恢复、账号密码输入保留、组件库内置空态语言。
- 动态镜像表单、容器删除确认、Compose 编辑、任务筛选和日志详情、备份策略、未验证快照恢复。
- 共 70 项截图/布局检查通过，无文档横向溢出、按钮文字裁切、浏览器脚本错误或翻译警告。

[嵌入式应用结果](embedded-results.json)：使用生产构建重新编译 Go 应用，在独立临时目录中创建一次性管理员，Docker endpoint 指向明确不存在的 Socket。验证真实中文初始化、英语登录及认证错误、断连状态隐藏统计、刷新后的语言偏好恢复，浏览器无脚本错误。临时服务、数据库、密钥和二进制在验收后清理。

代表截图：[英语初始化](en-initialization-390.png)、[英语工作台](en-dashboard-390.png)、[英语设置](en-settings-1440.png)、[镜像表单](en-image-form-390.png)、[删除确认](en-container-risk-390.png)、[未验证快照](en-unverified-restore-390.png)。

视觉验收采用 visual-verdict，对照现有 UI 截图检查布局与交互层级，评分 95/100；结构化结果位于 `.omx/state/i18n/ralph-progress.json`。

## 复现

使用项目现有工具链；Playwright 引用已有安装，不增加项目测试依赖。

```sh
pnpm --dir web dev --port 18108
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/i18n/verify.cjs
```

嵌入式检查需要独立的临时数据库和不存在的 Docker endpoint，默认连接 `http://127.0.0.1:18109`，可通过 `DM_QA_URL` 覆盖。脚本要求尚未初始化的临时应用，会创建并登录一次性管理员。

```sh
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/i18n/embedded.cjs
```

## 剩余限制

现有 Vite 主分包超过 500 kB 的提示仍存在；本次构建主 JS 为约 619 kB，gzip 约 190 kB。两种词典随应用一起加载，以保证切换即时可用。本次验证不重跑涉及真实 Docker 写入的发布矩阵，后端逻辑未变。
