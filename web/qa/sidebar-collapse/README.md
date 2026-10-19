# 桌面侧栏折叠验收

日期：2026-10-19。基于现有Vue/Naive UI导航实现，没有新增依赖、API或后端变更。

## 功能与修改文件

- `web/src/App.vue`：在桌面顶栏左侧增加折叠/展开按钮，支持原生按钮的Enter/Space操作，保留焦点。展开208px，折叠64px，内容区自动扩宽；折叠后只显示图标，菜单按钮仍有完整的aria-label及原生title悬停提示。
- 状态保存到localStorage的 `docker-manager:sidebar-collapsed`，默认展开。刷新和页面跳转保留选择；存储读取/写入异常时仍可在当前页面正常切换。
- 复用现有800px移动断点。手机始终显示完整文字抽屉，桌面折叠偏好保持不变；从手机返回桌面恢复原状态。跨断点时在桌面折叠按钮/手机菜单按钮之间转移焦点。
- 折叠时隐藏品牌文字、本地主机装饰块、分组说明和账号副文案；保留品牌入口、九项导航及退出按钮，并补全名称提示。已有移动焦点循环、Escape、遮罩关闭和inert逻辑保留。
- `web/src/style.css`：以共享侧栏宽度变量同步布局，增加简短宽度过渡和折叠样式；继续遵守减少动态效果偏好。
- `web/src/components/AppIcon.vue`：复用本地SVG组件，增加两个展开/折叠图标。
- `web/src/i18n/common.ts`：增加中英文折叠/展开文案。
- `web/src/App.test.ts`：增加持久化、路由、存储失败、无效值和移动断点独立性回归。

## 验证

- 68项前端测试（12个文件）全部通过；lint/类型检查和生产构建通过；脚本语法检查与 `git diff --check` 通过。
- 完整120项既有浏览器检查通过：展开状态中英文9页面×5视口、弹层、错误/加载/未知数据、移动键盘和确认交互；错误0。
- 新增72项折叠路由检查：中英文×1440×900、1366×768、1280×800、801×900×9页面；无文档横向溢出，图标位于侧栏内，所有入口可点击且唯一选中。
- 在生产构建预览上验证：Enter折叠、Space展开、按钮焦点保留、刷新恢复、展开选择保存、折叠退出提示、400px短屏退出可达。
- 验证800px断点及390px手机：抽屉248px、文字完整，Tab/Shift+Tab焦点循环、Escape返回菜单按钮、导航后关闭、背景inert，返回桌面恢复折叠偏好。
- 验证侧栏偏好存储拒绝/配额错误时仍能切换；测试仅拦截侧栏偏好键，避免影响测试环境或无关功能的存储行为。单位测试另覆盖浏览器存储方法整体抛错。
- 合计192项路由/布局检查及附加交互断言，浏览器脚本错误0。数据全部是浏览器拦截的测试数据，没有真实Docker变更。

## 证据与复现

[中文折叠效果](collapsed-zh-CN.png)、[英文折叠效果](collapsed-en.png)、[手机菜单](mobile-drawer.png)。详见 `collapse-results.json`、`fixture-results.json` 和 `visual-verdict.json`；视觉评分为设计目标自评，不是参考图像素还原。

使用现有Playwright/Chrome，不添加项目依赖：

```sh
pnpm --dir web build
pnpm --dir web exec vite preview --host 127.0.0.1 --port 18099
DM_QA_URL=http://127.0.0.1:18099 DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/sidebar-collapse/verify.cjs
DM_QA_URL=http://127.0.0.1:18099 DM_QA_OUTPUT_DIR="$PWD/web/qa/sidebar-collapse" DM_QA_PLAYWRIGHT=/absolute/path/to/playwright node web/qa/compact-console/fixtures.cjs
```

完整截图保留在本地，Git仅保存代表截图；前几轮视觉证据和旧二进制保留。原生title提示由浏览器控制显示时机；存储被禁用时偏好仅在当前页面有效。已有Vite主包超过500kB提示保留，本轮未扩展到分包重构。

## 二进制包

最新Linux AMD64/ARM64包位于 `bin/release/20261019-101059/`，已逐字节检查嵌入最新前端，架构/包内容/权限/SHA256校验通过，见 `package-results.json`。旧包保留；未执行真实Linux或Docker验收。自有开发/预览服务、临时工具链均已清理。
