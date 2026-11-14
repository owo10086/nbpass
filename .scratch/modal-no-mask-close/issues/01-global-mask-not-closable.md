Status: resolved
Blocked by: none

# 01 全局禁止点遮罩关闭弹窗与业务抽屉

规格：`.scratch/modal-no-mask-close/spec.md`（实现决策与测试决策以规格为准）。

## 要做的事

- 应用根部 AntD ConfigProvider 增加 Modal 与 Drawer 的全局 `mask.closable = false`。
- 删除各组件上 `maskClosable={...}` 的单独设置（当前 9 处：端口转发编辑、服务器组编辑、批量导入端口转发、端点编辑、端点组编辑、端点被墙处理、转发链编辑、转发链删除确认、证书与凭据轮换）；保留 `closable` / `keyboard` / 取消按钮禁用逻辑。
- 移动端主导航抽屉显式 `mask={{ closable: true }}`。
- DESIGN.md 表单 / 弹窗规范处增加交互规则一条（措辞见规格）。

## 验收

- `web/src` 中除主导航抽屉外，不再有组件把遮罩关闭打开。
- `cd web && npm run build` 通过；`npm test` 与 `web/src/*.test.mjs`、`web/*.test.mjs` 现有测试通过。

## Comments

- 2026-11-14：由 Codex 在提交 60dabb6 实现；build、npm test 与全部 .test.mjs 通过；Codex 只读审查（规格符合度、代码规范）无问题。
