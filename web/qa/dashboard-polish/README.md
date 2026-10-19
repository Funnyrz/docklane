# 工作台留白与层次优化

2026-10-19。针对用户实际截图中主机信息过密的问题，只调整工作台，不改变单行导航和其他页面密度。

## 修改文件与简化

- `web/src/style.css`：用桌面两列主机字段替换紧贴的八行表格；删除1px行内边距规则，标签和值分别排版。手机仍为单列，行内增加留白。
- 指标卡约97px高，数字32px，图标改为右上角轻量底色；区块间距16px，面板内边距16–18px、圆角10px和轻阴影。主机名用小标签呈现，备份摘要用浅色分区，任务类型列固定宽度以对齐路径。
- `web/src/views/DashboardView.vue`：仅调整指标标签和图标顺序；API、数据、状态、轮询和交互行为不变。未新增依赖。
- `web/qa/compact-console/fixtures.cjs`：增加可选 `DM_QA_OUTPUT_DIR`，复用完整验收脚本而不覆盖上一版截图。
- `single-task.cjs`：复现用户截图的4容器/3运行/8镜像/0卷、暂无验证备份和1条成功任务；截图等待字体和布局稳定。

## 验证

- 修改前12项App/工作台行为回归通过；修改后全部63项前端测试、lint/类型检查、生产构建通过。`git diff --check`及浏览器脚本语法检查通过。
- 完整复用120项浏览器检查：中英文9页面×5视口、移动/桌面弹层、加载、断连、未知值、超长字段与错误。另有2项与用户截图相同的稀疏数据检查；共122项，浏览器脚本错误0。
- 中英文1440×900保留完整八项主机信息、备份提示和五条任务；正常数据页面不纵向滚动。主机面板约291px、最近任务约244px、第五任务底部约783px、页脚底部约866px，见 `first-screen.json`。
- 手机单列字段保持完整，长内容自然换行；保留空值语义、危险确认和移动导航焦点检查。
- [与用户截图相同数据量的桌面效果](single-task-1440.png)、[手机效果](single-task-390.png)、[五任务工作台](preview.png)、[英文效果](fixture-dashboard-en-1440x900.png)。所有截图为浏览器测试数据，未连接真实Docker主机。
- 视觉自评见 `visual-verdict.json`，用于检查拥挤问题和设计目标，不是旧截图的像素复刻评分。

## 复现

启动开发服务后执行，使用已有Playwright/Chrome，无新项目依赖：

```sh
pnpm --dir web dev --port 18098
DM_QA_OUTPUT_DIR="$PWD/web/qa/dashboard-polish" DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/compact-console/fixtures.cjs
DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/dashboard-polish/single-task.cjs
```

全部矩阵截图在本地保存；Git只保留代表截图。上一版验收和发布包保留原样。

限制：已有Vite主包超过500kB提示仍存在，本轮未做分包重构；较矮屏幕或异常/超长数据允许纵向滚动。Linux包为交叉编译，不代表本轮执行了Linux原生或真实Docker功能验收。

## 更新后的二进制包

沿用此前已授权的本地打包流程，将最终界面嵌入Linux AMD64/ARM64精简包，输出 `bin/release/20261019-100351/`。旧包保留。架构、包内二进制与LICENSE、可执行权限、SHA256及最新前端逐字节嵌入检查均通过，见 `package-results.json`。未发布到远程。自有开发服务和临时工具链已清理。
