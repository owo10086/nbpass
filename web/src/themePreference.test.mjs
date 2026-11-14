import assert from 'node:assert/strict';
import test from 'node:test';
import { createThemePreferenceStore } from './themePreference.ts';

const storageKey = 'nbpass.theme';

function browserFixture({ dark = false, saved = null, readFails = false, writeFails = false, storageDenied = false } = {}) {
  const values = new Map(saved === null ? [] : [[storageKey, saved]]);
  const storage = {
    getItem: key => { if (readFails) throw new Error('Read denied'); return values.get(key) ?? null; },
    setItem: (key, value) => { if (writeFails) throw new Error('Write denied'); values.set(key, value); },
  };
  const media = new EventTarget();
  media.matches = dark;
  const browser = new EventTarget();
  Object.defineProperty(browser, 'localStorage', { get: () => { if (storageDenied) throw new Error('Storage denied'); return storage; } });
  browser.matchMedia = query => { assert.equal(query, '(prefers-color-scheme: dark)'); return media; };
  return {
    browser, storage, values,
    changeSystem: dark => { media.matches = dark; media.dispatchEvent(new Event('change')); },
    changeStorage: (newValue, { key = storageKey, storageArea = storage, currentValue = newValue } = {}) => {
      if (storageArea === storage) {
        if (key === null) values.clear();
        else if (currentValue === null) values.delete(key);
        else values.set(key, currentValue);
      }
      const event = new Event('storage');
      Object.assign(event, { key, newValue, storageArea });
      browser.dispatchEvent(event);
    },
  };
}

test('首次访问使用系统外观，损坏的偏好也回退到系统', () => {
  for (const [dark, saved, theme] of [[false, null, 'light'], [true, null, 'dark'], [false, 'invalid', 'light']]) {
    const { browser } = browserFixture({ dark, saved });
    assert.deepEqual(createThemePreferenceStore(browser).getSnapshot(), { preference: 'system', theme });
  }
});

test('重新打开时恢复浏览器已保存的手动选择', () => {
  for (const [dark, saved] of [[false, 'dark'], [true, 'light']]) {
    const { browser } = browserFixture({ dark, saved });
    assert.deepEqual(createThemePreferenceStore(browser).getSnapshot(), { preference: saved, theme: saved });
  }
});

test('手动选择立即通知订阅者并持久保存，后续实例恢复相同外观', () => {
  const { browser, values } = browserFixture();
  const store = createThemePreferenceStore(browser);
  const observed = [];
  const unsubscribe = store.subscribe(() => observed.push(store.getSnapshot()));
  store.setPreference('dark');
  assert.deepEqual(observed, [{ preference: 'dark', theme: 'dark' }]);
  assert.equal(values.get(storageKey), 'dark');
  assert.deepEqual(createThemePreferenceStore(browser).getSnapshot(), { preference: 'dark', theme: 'dark' });
  unsubscribe();
  store.setPreference('light');
  assert.equal(observed.length, 1);
});

test('跟随系统实时变化，手动模式不被系统变化覆盖', () => {
  const fixture = browserFixture();
  const store = createThemePreferenceStore(fixture.browser);
  fixture.changeSystem(true);
  assert.deepEqual(store.getSnapshot(), { preference: 'system', theme: 'dark' });
  store.setPreference('light');
  fixture.changeSystem(false);
  fixture.changeSystem(true);
  assert.deepEqual(store.getSnapshot(), { preference: 'light', theme: 'light' });
  store.setPreference('system');
  assert.deepEqual(store.getSnapshot(), { preference: 'system', theme: 'dark' });
});

test('其他标签页修改同步更新，但无关键和 sessionStorage 事件不影响当前选择', () => {
  const fixture = browserFixture();
  const store = createThemePreferenceStore(fixture.browser);
  assert.deepEqual(store.getSnapshot(), { preference: 'system', theme: 'light' });
  fixture.changeStorage('dark');
  assert.deepEqual(store.getSnapshot(), { preference: 'dark', theme: 'dark' });
  fixture.changeStorage('light', { key: 'other' });
  fixture.changeStorage('light', { storageArea: {} });
  assert.deepEqual(store.getSnapshot(), { preference: 'dark', theme: 'dark' });
});

test('迟到的标签页事件读取最新偏好，避免旧事件覆盖更新的选择', () => {
  const fixture = browserFixture({ saved: 'dark' });
  const store = createThemePreferenceStore(fixture.browser);
  fixture.changeStorage('light', { currentValue: 'system' });
  assert.deepEqual(store.getSnapshot(), { preference: 'system', theme: 'light' });
});

test('其他标签页删除偏好、清空存储或写入无效值时恢复跟随系统', () => {
  for (const [value, options] of [[null, {}], [null, { key: null }], ['invalid', {}]]) {
    const fixture = browserFixture({ dark: false, saved: 'dark' });
    const store = createThemePreferenceStore(fixture.browser);
    fixture.changeStorage(value, options);
    assert.deepEqual(store.getSnapshot(), { preference: 'system', theme: 'light' });
  }
});

test('本地存储读取、写入或访问失败时仍可在本页切换', () => {
  for (const options of [{ readFails: true }, { writeFails: true }, { storageDenied: true }]) {
    const { browser } = browserFixture(options);
    const store = createThemePreferenceStore(browser);
    assert.deepEqual(store.getSnapshot(), { preference: 'system', theme: 'light' });
    assert.doesNotThrow(() => store.setPreference('dark'));
    assert.deepEqual(store.getSnapshot(), { preference: 'dark', theme: 'dark' });
  }
});

test('未变化的主题保持快照引用稳定，避免订阅无限重渲染', () => {
  const fixture = browserFixture();
  const store = createThemePreferenceStore(fixture.browser);
  const snapshot = store.getSnapshot();
  assert.equal(store.getSnapshot(), snapshot);
  fixture.changeSystem(false);
  assert.equal(store.getSnapshot(), snapshot);
});
