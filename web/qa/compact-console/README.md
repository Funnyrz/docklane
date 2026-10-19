# Docklane 紧凑浅色控制台验收

日期：2026-10-19（Asia/Shanghai）。本目录独立保存新版证据，`../ui-upgrade/` 等历史验收保持原样。

## 修改与简化

- `web/src/App.vue`：208px 浅色侧栏、单行菜单、52px 顶栏；删除菜单副标题、重复选中箭头、页标题副标题和品牌装饰行。工作台自行呈现合并摘要，其他页保留标题。移动导航焦点、Escape、遮罩、返回和 inert 行为保留。
- `web/src/views/DashboardView.vue`：合并工作台标题/主机/状态/刷新，删除大横幅、指标副标题及备份宣传文案；保留四项指标、八项主机信息、真实备份覆盖/验证日期、一致性警示和五条任务。任务长路径省略并提供完整 title。
- `web/src/style.css`、`web/src/theme.ts`、`web/index.html`：直接替换旧间距、渐变与悬浮样式，统一中性色、细边框、6–8px圆角、12px区块间距、20px页面边距、表格与弹层密度。移动端默认控件40px，小按钮36px、菜单44px；登录页同步视觉。
- `web/src/App.test.ts`、`web/src/views/DashboardView.test.ts`：增加行为回归。无新依赖、业务API、数据库或后端代码变更。

## 首屏证明

[1440×900 中文工作台](preview.png)、[英文工作台](fixture-dashboard-en-1440x900.png)。均为浏览器测试数据，非真实生产环境截图。

实际几何数据见 `first-screen.json`：主机摘要48px、指标区84px、主机/保护面板约255px、最近任务约232px；第五条任务底部约715px，页脚底部约794px，文档高度900px，无页面纵向滚动。主机面板采用自然高度，未为达到目标裁切字段或警示。中英文均有首屏断言。

## 验证结果

- 修改前54项前端测试；修改后63项、12个文件全部通过。lint与类型检查（项目lint为vue-tsc）、生产构建通过。
- Go `test ./...`、`vet ./...` 通过；使用项目指定Go1.27.2构建嵌入最终前端的服务，并通过真实HTTP资源检查。
- `git diff --check`、两份浏览器脚本语法检查通过。八组主要文字/背景对比度均≥4.5:1，见 `contrast-results.json`；不等同于完整可访问性认证。
- 90组有内容布局：9个页面 × 中英文 × 1440×900、1366×768、1280×800、768×1024、390×844。验证无文档横向溢出、单行菜单完整、桌面资源操作列及按钮可见、端口列无重叠。
- 24组弹层：390/1440px的容器配置、日志、终端、删除确认、Compose编辑器、表单错误、任务筛选、任务详情、备份策略、快照、未验证恢复和叠加风险确认。
- 6组状态：断连、加载，以及390/1440px的未知值/空任务、超长主机/路径/错误信息；错误信息完整呈现并允许页面增长。
- 合计120组截图检查；移动焦点循环/返回、断点恢复、确认按钮默认禁用与取消聚焦、等宽编辑器、40px移动刷新按钮均通过；浏览器脚本错误0。见 `fixture-results.json`。
- 独立Go服务：初始化、退出、登录，9页×4宽度（36组）、移动导航、400px短屏侧栏滚动与退出可达；浏览器脚本错误0。最终嵌入脚本为 `index-BHYEXwNo.js`，见 `browser-results.json`。
- 截图自评和只读独立复核未发现未解决问题；`visual-verdict.json`明确标注无外部参考图，分数不是像素还原评分。

## 复现

需要现有pnpm、项目要求的Go1.27.2，以及已有Playwright/Chrome。无需安装新的项目依赖。

```sh
pnpm --dir web build
go build -o /tmp/docklane-compact-server ./cmd/server
DM_LISTEN=127.0.0.1:18099 DM_DATA_DIR=/tmp/docklane-compact-fresh-data DM_DOCKER_ENDPOINT=unix:///tmp/docklane-compact-no-engine.sock /tmp/docklane-compact-server
```

使用全新且仅用于验收的数据目录；在另一个终端：

```sh
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/compact-console/verify.cjs
DM_QA_URL=http://127.0.0.1:18099 DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/compact-console/fixtures.cjs
```

`verify.cjs`覆盖真实认证和断连页面；重验已有会话可设置 `DM_QA_REUSE_SESSION=1`。会话存入0600权限的 `/tmp/docklane-compact-browser-state.json`，验收后删除。`fixtures.cjs`独立拦截全部API（含认证），仅在浏览器模拟数据与四次预览/失败写请求；测试流文本也为模拟数据。本轮未执行真实Docker资源变更、终端连接、备份或恢复。

所有矩阵截图均保存在本目录本地，Git只保留代表截图，完整检查清单保存在JSON。可查看[容器列表](fixture-containers-zh-CN-1440x900.png)、[手机工作台](fixture-dashboard-zh-CN-390x844.png)、[恢复确认](fixture-restore-risk-confirm-zh-CN-390x844.png)、[超长数据与错误](fixture-long-values-error-zh-CN-1440x900.png)。

## 限制

已有Vite主JS超过500kB的体积提示仍存在（当前约618kB，gzip190kB）；本轮未扩展到分包重构。异常和超长数据、较矮视口允许正常纵向滚动。所有自有临时服务、数据库、认证会话、工具链与二进制均在验收后清理；构建资源和截图保留。
