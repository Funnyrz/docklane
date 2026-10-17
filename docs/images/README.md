# Docklane images / 项目图片

## Promotional images / 宣传图

- `zh/promo.png`: Chinese promotional image supplied by the project maintainer on 2026-10-17.
- `en/promo.png`: English promotional image supplied by the project maintainer on 2026-10-17.
- Both PNG originals are retained byte-for-byte at 1536 × 1024 pixels, without image edits.
- These are promotional concept images, not application screenshots or evidence of implemented features. Refer to the README feature descriptions and real screenshots for current capabilities.

两张宣传图由项目维护者于 2026-10-17 提供，按语言分别收录，原始 PNG 未经修改。图片包含部分尚未实现的功能展示，不能作为功能验收依据；当前能力以双语 README 正文及真实截图为准。

## Actual screenshots / 真实界面截图

Captured on 2026-10-17 from the current production frontend build using a local read-only API proxy to the real test environment. No mock resources or metrics were added. The test server was not redeployed.

截图来自本地生产构建，通过只读代理读取测试环境真实 API。未添加模拟资源或指标，未重新部署测试服务器。仅登录、退出及读取页面，不执行 Docker 资源变更。

- `zh/`: Simplified Chinese / 简体中文
- `en/`: English / 英文
- Six pages per language: overview, containers, Compose, images, backups, settings.
- Original browser JPEG captures are retained without image edits. Viewport: 1265 × 712 CSS pixels; selected pages use full-page capture.
- Compose and settings use viewport captures to omit raw discovery configuration and audit details. Empty states and restricted capabilities reflect the real host.
- No login credentials or test-service address are included. Historical QA screenshots retain their original brand as evidence.

Visual review passed at 95/100 after correcting English container action-column clipping and sidebar footer spacing. All 12 captures were checked for brand, language, layout and excluded sensitive detail. This is a self-assessment of the new design direction, not an external certification or pixel-equivalence claim.

Validation for this capture: `make check` passed (Go tests, race, vet, frontend typecheck and 54 frontend tests), production frontend build passed, and a newly compiled Go server served the embedded JavaScript/CSS byte-for-byte from that build. The existing large-bundle advisory remains.
