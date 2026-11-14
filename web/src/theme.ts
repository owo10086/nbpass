import { theme, type ThemeConfig } from 'antd';
import type { EffectiveTheme } from './themePreference';

// 唯一 token 来源：CSS 变量和 AntD 配置都从这里生成。
const darkTokens = {
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

export const tokens = {
  dark: darkTokens,
  light: {
    ...darkTokens,
    bg: '#f4f6f8', 'bg-elev': '#ffffff', 'bg-sunken': '#ffffff', 'bg-hover': '#edf0f4', 'bg-head': '#f0f2f5',
    line: '#e2e6ec', 'line-strong': '#b7c0cd', text: '#1d2533', 'text-2': '#4e5969', 'text-3': '#637083', muted: '#637083',
    accent: '#c2410c', 'accent-hover': '#a83a0a', 'accent-soft': 'rgba(194,65,12,.09)',
    ok: '#237b19', 'ok-soft': '#edf7e9', warn: '#946200', 'warn-soft': '#fff6df',
    danger: '#c72b32', 'danger-soft': '#fff0f0', info: '#2165c5', 'info-soft': '#edf4ff',
    'tag-bg': '#edf0f4', scrollbar: '#b7c0cd', 'avatar-bg': '#e2e7ee',
    'tag-green': '#237b19', 'tag-red': '#c72b32', 'tag-orange': '#a83a0a', 'tag-blue': '#2165c5',
    'tag-purple': '#7540ac', 'purple-soft': '#f5edff',
    'shadow-popup': '0 10px 28px rgba(29,37,51,.14)', 'topbar-bg': 'rgba(255,255,255,.92)',
  },
};

export function applyTheme(mode: EffectiveTheme) {
  const root = document.documentElement;
  for (const [name, value] of Object.entries(tokens[mode])) root.style.setProperty(`--${name}`, value);
  root.style.setProperty('--color-scheme', mode);
  root.style.colorScheme = mode;
  root.dataset.theme = mode;
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', tokens[mode].bg);
  // 既有业务样式的别名；保持同一来源，后续票可逐步换用标准变量。
  root.style.setProperty('--bg-subtle', 'var(--bg-sunken)');
  root.style.setProperty('--bg-sidebar', 'var(--bg-sunken)');
}

function createThemeConfig(mode: EffectiveTheme): ThemeConfig {
  const values = tokens[mode];
  return {
    // 算法负责背景与状态色阶；主色保持与 CSS 导航一致。
    algorithm: seed => ({ ...(mode === 'dark' ? theme.darkAlgorithm(seed) : theme.defaultAlgorithm(seed)), colorPrimary: values.accent }),
    token: {
      colorPrimary: values.accent, colorPrimaryHover: values['accent-hover'], colorPrimaryActive: values.accent,
      colorLink: values.accent, colorLinkHover: values['accent-hover'], colorInfo: values.info,
      colorText: values.text, colorTextSecondary: values['text-2'], colorTextTertiary: values['text-3'], colorTextQuaternary: values.muted,
      colorBgBase: values.bg, colorBgLayout: values.bg, colorBgContainer: values['bg-elev'], colorBgElevated: values['bg-elev'],
      colorBorder: values['line-strong'], colorBorderSecondary: values.line,
      colorSuccess: values.ok, colorWarning: values.warn, colorError: values.danger,
      borderRadius: parseInt(values.radius), borderRadiusLG: parseInt(values['radius-lg']), borderRadiusSM: parseInt(values['radius-sm']),
      fontSize: parseInt(values['font-size']), controlHeight: parseInt(values['control-height']),
      fontFamily: values.sans, fontFamilyCode: values.mono, boxShadowSecondary: values['shadow-popup'],
      motionDurationMid: '0.15s', motionDurationSlow: '0.2s',
    },
    components: {
      Button: { primaryShadow: 'none', defaultShadow: 'none', dangerShadow: 'none', fontWeight: 500, primaryColor: values.white },
      Table: { headerBg: values['bg-head'], headerColor: values.text, headerSplitColor: 'transparent', cellPaddingBlock: 12, cellPaddingInline: 8, rowHoverBg: values['bg-hover'], headerBorderRadius: 0 },
      Card: { headerFontSize: 14 }, Modal: { titleFontSize: 16 },
      Tag: { defaultBg: values['tag-bg'], defaultColor: values['text-2'] },
      Segmented: {
        trackBg: values['bg-sunken'], itemColor: values['text-2'],
        itemHoverBg: values['bg-hover'], itemHoverColor: values.text, itemActiveBg: values['accent-soft'],
        itemSelectedBg: values.accent, itemSelectedColor: values.white,
      },
    },
  };
}

export const themeConfigs = { light: createThemeConfig('light'), dark: createThemeConfig('dark') };
