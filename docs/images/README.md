# Docklane images / 项目图片

## Promotional images / 宣传图

- `zh/promo.png`: Chinese promotional image supplied by the project maintainer on 2026-10-17.
- `en/promo.png`: English promotional image supplied by the project maintainer on 2026-10-17.
- Both PNG originals are retained byte-for-byte at 1536 × 1024 pixels, without image edits.
- These are promotional concept images, not application screenshots or evidence of implemented features. Refer to the README feature descriptions and real screenshots for current capabilities.

两张宣传图由项目维护者于 2026-10-17 提供，按语言分别收录，原始 PNG 未经修改。图片包含部分尚未实现的功能展示，不能作为功能验收依据；当前能力以双语 README 正文及真实截图为准。

## Actual screenshots / 真实界面截图

Captured on 2026-10-19 directly from the deployed Docklane test application using an authenticated browser. The screenshots show the current compact console, dashboard, sidebar and capability settings. Resources and metrics come from the test environment without API interception or fixtures.

截图于 2026-10-19 直接采集自已部署的 Docklane 测试环境，展示新版控制台、工作台、侧栏及功能配置界面。资源和指标均来自真实环境，没有 API 拦截或模拟数据。采集期间仅登录、浏览页面、切换浏览器语言并退出，不执行 Docker 资源或配置变更。

- `zh/`: Simplified Chinese / 简体中文
- `en/`: English / 英文
- Six pages per language: overview, containers, Compose, images, backups, settings.
- Original browser JPEG captures are retained without image edits at their native exported dimensions. The default browser viewport is 1280 × 720 CSS pixels.
- Compose uses a shorter 1280 × 500 viewport to exclude raw discovery configuration. Settings uses the default viewport to omit lower diagnostics and audit details. Empty states and restricted capabilities reflect the real host.
- No login credentials or test-service address are included. Historical QA screenshots retain their original brand as evidence.

All 12 captures were visually checked for the Docklane brand, correct language, readable page headings, resource state and excluded sensitive detail. The supplied promotional images above remain unchanged.

Validation for this update covers JPEG integrity, matching image language and page, changed image hashes, unchanged promotional image hashes and README image/link targets. This screenshot update does not change application code; broader product verification is documented in [the verification report](../verification.md).
