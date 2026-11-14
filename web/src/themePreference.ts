export type ThemePreference = 'light' | 'dark' | 'system';
export type EffectiveTheme = 'light' | 'dark';
export type ThemeSnapshot = { preference: ThemePreference; theme: EffectiveTheme };

const storageKey = 'nbpass.theme';

function preferenceFrom(value: string | null): ThemePreference {
  return value === 'light' || value === 'dark' ? value : 'system';
}

export function createThemePreferenceStore(browser: Pick<Window, 'localStorage' | 'matchMedia' | 'addEventListener'>) {
  const media = browser.matchMedia('(prefers-color-scheme: dark)');
  let storage: Storage | undefined;
  let saved: string | null = null;
  try { storage = browser.localStorage; saved = storage.getItem(storageKey); } catch { /* 禁用存储时仍可在本页切换。 */ }
  const resolve = (preference: ThemePreference): EffectiveTheme => preference === 'system' ? (media.matches ? 'dark' : 'light') : preference;
  const preference = preferenceFrom(saved);
  let snapshot: ThemeSnapshot = { preference, theme: resolve(preference) };
  const listeners = new Set<() => void>();

  function update(preference: ThemePreference) {
    const theme = resolve(preference);
    if (snapshot.preference === preference && snapshot.theme === theme) return;
    snapshot = { preference, theme };
    listeners.forEach(listener => listener());
  }

  media.addEventListener('change', () => update(snapshot.preference));
  browser.addEventListener('storage', event => {
    if (!storage || event.storageArea !== storage || (event.key !== storageKey && event.key !== null)) return;
    let saved = event.newValue;
    // 事件可能排队；以当前值为准，防止旧事件覆盖之后的选择。
    try { saved = storage.getItem(storageKey); } catch { /* 读取受限时使用事件携带的值。 */ }
    update(preferenceFrom(saved));
  });

  return {
    getSnapshot: () => snapshot,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    setPreference: (preference: ThemePreference) => {
      try { storage?.setItem(storageKey, preference); } catch { /* 写入失败不阻止当前界面切换。 */ }
      update(preference);
    },
  };
}
