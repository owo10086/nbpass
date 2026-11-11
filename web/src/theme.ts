import { theme, type ThemeConfig } from 'antd';

// 唯一 token 来源：CSS 变量和 AntD 配置都从这里生成。
export const tokens = {
  bg: '#04060a', 'bg-elev': '#0b0f17', 'bg-sunken': '#070a10', 'bg-hover': '#161c27', 'bg-head': '#1a2130',
  line: '#252c39', 'line-strong': '#343c4b', text: '#e7e9ee', 'text-2': '#b5bcc8', 'text-3': '#7d8696', muted: '#7d8696',
  accent: '#ff6a13', 'accent-hover': '#ff8540', 'accent-soft': 'rgba(255,106,19,.14)',
  ok: '#52c41a', 'ok-soft': 'rgba(82,196,26,.12)', warn: '#f0b44c', 'warn-soft': '#241c0f',
  danger: '#ff4d4f', 'danger-soft': 'rgba(255,77,79,.12)', info: '#4c8dff', 'info-soft': 'rgba(76,141,255,.12)',
  white: '#ffffff', 'tag-bg': '#141a24', 'scrollbar': '#2a3140', 'avatar-bg': '#2b3344',
  'tag-green': '#73d13d', 'tag-red': '#ff7875', 'tag-orange': '#ff9a52', 'tag-blue': '#7aa9ff',
  'tag-purple': '#b38bff', 'purple-soft': 'rgba(146,84,222,.14)',
  radius: '6px', 'radius-lg': '8px', 'radius-sm': '4px', side: '224px', 'topbar-height': '56px',
  'font-size': '14px', 'font-small': '12px', 'font-description': '13px', 'font-title': '24px', 'font-section': '16px',
  'control-height': '32px', 'space-xs': '8px', 'space-sm': '12px', 'space-md': '16px', 'space-lg': '24px', 'space-xl': '32px',
  sans: '-apple-system, BlinkMacSystemFont, "PingFang SC", "Helvetica Neue", "Microsoft YaHei", sans-serif',
  mono: '"SF Mono", Menlo, Consolas, monospace',
  'shadow-popup': '0 10px 28px rgba(0,0,0,.55)', 'topbar-bg': 'rgba(4,6,10,.88)',
};

export function applyTheme() {
  const root = document.documentElement;
  for (const [name, value] of Object.entries(tokens)) root.style.setProperty(`--${name}`, value);
  // 既有业务样式的别名；保持同一来源，后续票可逐步换用标准变量。
  root.style.setProperty('--bg-subtle', 'var(--bg-sunken)');
  root.style.setProperty('--bg-sidebar', 'var(--bg-sunken)');
}

export const themeConfig: ThemeConfig = {
  // Dark 算法负责背景与状态色阶；主色保持与 CSS 导航一致。
  algorithm: seed => ({ ...theme.darkAlgorithm(seed), colorPrimary: tokens.accent }),
  token: {
    colorPrimary: tokens.accent, colorPrimaryHover: tokens['accent-hover'], colorPrimaryActive: tokens.accent,
    colorLink: tokens.accent, colorLinkHover: tokens['accent-hover'], colorInfo: tokens.info,
    colorText: tokens.text, colorTextSecondary: tokens['text-2'], colorTextTertiary: tokens['text-3'], colorTextQuaternary: tokens.muted,
    colorBgBase: tokens.bg, colorBgLayout: tokens.bg, colorBgContainer: tokens['bg-elev'], colorBgElevated: tokens['bg-elev'],
    colorBorder: tokens['line-strong'], colorBorderSecondary: tokens.line,
    colorSuccess: tokens.ok, colorWarning: tokens.warn, colorError: tokens.danger,
    borderRadius: parseInt(tokens.radius), borderRadiusLG: parseInt(tokens['radius-lg']), borderRadiusSM: parseInt(tokens['radius-sm']),
    fontSize: parseInt(tokens['font-size']), controlHeight: parseInt(tokens['control-height']),
    fontFamily: tokens.sans, fontFamilyCode: tokens.mono, boxShadowSecondary: tokens['shadow-popup'],
    motionDurationMid: '0.15s', motionDurationSlow: '0.2s',
  },
  components: {
    Button: { primaryShadow: 'none', defaultShadow: 'none', dangerShadow: 'none', fontWeight: 500, primaryColor: tokens.white },
    Table: { headerBg: tokens['bg-head'], headerColor: tokens.text, headerSplitColor: 'transparent', cellPaddingBlock: 12, cellPaddingInline: 8, rowHoverBg: tokens['bg-hover'], headerBorderRadius: 0 },
    Card: { headerFontSize: 14 }, Modal: { titleFontSize: 16 },
    Tag: { defaultBg: tokens['tag-bg'], defaultColor: tokens['text-2'] },
  },
};
