# Docklane images / 项目图片

## Promotional images / 宣传图

- `zh/promo.png`: Chinese promotional image supplied by the project maintainer on 2026-10-17.
- `en/promo.png`: English promotional image supplied by the project maintainer on 2026-10-17.
- Both PNG originals are retained byte-for-byte at 1536 × 1024 pixels, without image edits.
- These are promotional concept images, not application screenshots or evidence of implemented features. Refer to the README feature descriptions and real screenshots for current capabilities.

两张宣传图由项目维护者于 2026-10-17 提供，按语言分别收录，原始 PNG 未经修改。图片包含部分尚未实现的功能展示，不能作为功能验收依据；当前能力以双语 README 正文及真实截图为准。

## Actual screenshots / 真实界面截图

Captured on 2026-10-19 directly from the latest deployed Docklane using an authenticated browser. These screenshots show the collapsible navy sidebar, live resource trends, container preview, compact lists and tabbed capability settings. Data comes from the actual test host, without API interception or demo fixtures. Trends accumulated through normal 15-second polling in the current page session.

截图于 2026-10-19 直接采集自已部署的最新版 Docklane，展示深蓝可折叠侧栏、真实资源曲线、容器概览、紧凑列表和设置页三个标签。资源及指标来自真实测试主机，没有拦截 API、填充演示数据或制造历史曲线；曲线由页面每 15 秒正常采样积累。保留真实空状态与缺少工具、未配置、未授权状态。

- `zh/`: Simplified Chinese / 简体中文。
- `en/`: English / 英文。
- Six pages per language: overview, containers, Compose, images, backups and settings.
- Original browser JPEG exports are retained without post-processing or image edits. Screenshots use the current browser viewport; they show the actual first screen rather than forcing the entire dashboard into one image.
- Compose uses a 1391 × 500 CSS-pixel viewport to exclude raw discovery configuration. The browser viewport override is reset afterward. Settings shows the complete default Overview & capabilities tab, including all six capability rows; diagnostics and audit are separate tabs.
- The supplied promotional PNGs remain unchanged. They are concept images rather than evidence of current features.

采集期间仅登录、浏览、切换浏览器语言并退出。没有启动、停止或删除容器，没有保存配置、修改授权、运行诊断或触发备份。截图不包含测试服务地址、登录凭据、日志、终端、原始 Compose 配置或审计详情。

All 12 captures were visually checked for correct language, page, resource state, legible headings and clean screenshot composition. Validation covers JPEG integrity/dimensions, updated image hashes, unchanged promotional image hashes, matching screenshot sets and README image/link targets. This update changes documentation and images only; broader product verification is documented in [the verification report](../verification.md).
