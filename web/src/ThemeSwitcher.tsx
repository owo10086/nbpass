import { useSyncExternalStore } from 'react';
import { Button, Dropdown } from 'antd';
import { DesktopOutlined, MoonOutlined, SunOutlined } from '@ant-design/icons';
import { createThemePreferenceStore, type ThemePreference } from './themePreference';

// 与登录状态无关，确保登录、退出和页面切换不重置偏好。
export const themePreferenceStore = createThemePreferenceStore(window);

const options = [
  { key: 'light', label: '日间', icon: <SunOutlined /> },
  { key: 'dark', label: '夜间', icon: <MoonOutlined /> },
  { key: 'system', label: '跟随系统', icon: <DesktopOutlined /> },
];

export function ThemeSwitcher() {
  const { preference } = useSyncExternalStore(themePreferenceStore.subscribe, themePreferenceStore.getSnapshot);
  const current = options.find(option => option.key === preference)!;
  return <Dropdown trigger={['click']} placement="bottomRight" menu={{
    items: options,
    selectable: true,
    selectedKeys: [preference],
    onClick: ({ key }) => themePreferenceStore.setPreference(key as ThemePreference),
  }}>
    <Button type="text" className="theme-switcher" icon={current.icon} aria-label={`切换主题，当前：${current.label}`}>
      <span className="theme-switcher-label">{current.label}</span>
    </Button>
  </Dropdown>;
}
