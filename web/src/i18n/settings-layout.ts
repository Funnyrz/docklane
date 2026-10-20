import type { MessageSchema } from './common'

export const settingsLayoutZh = {
  tabsLabel: '系统设置分类', overview: '概览与能力', diagnostics: '镜像诊断', audit: '操作审计',
  function: '功能名称', status: '状态', reason: '简短原因', action: '操作', details: '详情',
  detailsFor: '查看{name}详情', resolution: '解决方法', pathsCount: '已配置 {count} 个目录', mappingsCount: '已配置 {count} 条映射',
  viewAll: '查看全部', close: '关闭详情', diagnosisResult: '诊断结果', configResult: '配置与操作指引', previewResult: '配置预览',
  diagnosticSummary: '网络：{network} · Registry API：{registry} · {pull}',
  passed: '通过', failed: '失败', notTested: '未检测', pullSubmitted: '拉取任务已提交', pullNotTested: '镜像拉取未测试',
  configRead: '已读取当前 Docker 配置。', configInstructions: '当前仅提供宿主机手工操作指引。', configUnknown: '配置状态待确认，请查看详情。',
  previewReady: '预览已生成。请核对修改内容，再确认应用并重启 Docker。',
  readOnly: '此处仅展示当前生效的目录访问范围。修改后需重启 Docklane 生效。',
}
export const settingsLayoutEn: MessageSchema<typeof settingsLayoutZh> = {
  tabsLabel: 'Settings sections', overview: 'Overview & capabilities', diagnostics: 'Mirror diagnostics', audit: 'Operation audit',
  function: 'Capability', status: 'Status', reason: 'Reason', action: 'Action', details: 'Details',
  detailsFor: 'View {name} details', resolution: 'Resolution steps', pathsCount: '{count} directories configured', mappingsCount: '{count} mappings configured',
  viewAll: 'View all', close: 'Close details', diagnosisResult: 'Diagnostic results', configResult: 'Configuration & instructions', previewResult: 'Configuration preview',
  diagnosticSummary: 'Network: {network} · Registry API: {registry} · {pull}',
  passed: 'Passed', failed: 'Failed', notTested: 'Not checked', pullSubmitted: 'Pull task submitted', pullNotTested: 'Image pull not tested',
  configRead: 'The current Docker configuration has been read.', configInstructions: 'Only manual host instructions are available.', configUnknown: 'Configuration status needs review. Open details.',
  previewReady: 'The preview is ready. Review the changes before confirming and restarting Docker.',
  readOnly: 'These are the currently effective directory permissions. Restart Docklane after saving changes to activate them.',
}
