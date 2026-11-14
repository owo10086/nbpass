# 01 批量导入弹窗增加「文本导入」页签

Status: resolved
Blocked by: 无
Migration: 无（后端不改）

## 目标

按 `../spec.md` 实现 `规则名#监听端口#目标地址#目标端口` 文本批量导入，复用现有 JSON 导入的预检 / 导入接口与整批原子语义。

## 改动点

- 新增 `web/src/RuleTextImport.ts`：文本解析（含原文行号与本地校验错误）与 format 3 payload 生成；先写 `web/src/RuleTextImport.test.mjs`（TDD，红 → 绿）。
- `web/src/RulesTransfer.tsx`：弹窗内增加「文本导入」页签；整批选入口（入口节点 / 入口组）、链路形态（直连 / 单跳端点组 / 链式转发链）、允许私网目标（默认关）；本地错误按行号展示且不发请求；服务端预检逐条结果映射回原文行号；预检通过才可确认导入（`mode=create`）。入口 / 端点组 / 转发链选项来源复用规则新建表单（`web/src/Rules.tsx`）已有的数据。

## 验收

- `node --experimental-strip-types --test web/src/RuleTextImport.test.mjs` 通过，且现有 `web/src/*.test.mjs` 不回归。
- `npm --prefix web run build` 通过。
- `go test ./internal/panel/... ./internal/clusterharness/...` 不回归（后端未改，应全绿）。
- 浏览器验收由编排者在本机 `http://127.0.0.1` 面板完成。

## Comments

- 已按 TDD 完成 `RuleTextImport.ts` 与 9 项纯模块测试，覆盖格式、UTF-8 名称字节上限、端口、空行与原文行号、500 条上限、两种入口与三种链路、私网选项及服务端结果行号映射；先运行确认缺少模块 / 校验 / payload / 映射导致失败，再实现并通过。
- 现有弹窗新增「文本导入」页签，直接接收 `Rules.tsx` 已加载的入口节点、入口组、端点组、转发链；固定新增并启用全部规则。任一本地解析错误均阻止请求；服务端结果按数组位置映射原文行号；输入、共用选项或页签变化后清除预检结果。后端无改动，随代码更新仓库已跟踪的前端构建产物。
- 验收：`node --experimental-strip-types --test web/src/RuleTextImport.test.mjs` 为 9/9 通过；`node --experimental-strip-types --test web/src/*.test.mjs web/*.test.mjs` 为 65/65 通过；缺少依赖后执行 `npm --prefix web ci --no-fund --no-audit` 成功（安装 90 个包）；`npm --prefix web run build` 通过（3164 modules，仅有资源包超过 500 kB 的提示）；`go vet ./...` 通过，无输出；`git diff --check` 通过。
- `go test ./internal/panel/... ./internal/clusterharness/...` 已完整运行，退出码 1：`internal/panel` 通过（115.699s），`internal/clusterharness/fakedns` 通过（2.205s），`internal/clusterharness` 失败（464.540s）。失败项为 `TestIXMemberReal02NodePublicCompatibilityAndUpgradeConfirmation`（`非普通源码文件 pax_global_header`）与 `TestEntryReadinessRequiresAnAuthorizedSuccessfulExitForEveryTunnelRule`（恢复期连续成功断言）。单独复跑前者仍失败；已确认历史源码的 `git archive` 首项是类型 `g` 的 PAX 全局头，现有夹具只接受目录 / 普通文件。后者用 `-count=3` 单独复跑连续通过（7.360s）。后端文件与 HEAD 无差异，未扩大本工单范围修改这些测试。
- 功能实现未偏离规格；后端整套验收未全绿，原因与复跑结果如上。浏览器验收仍按工单安排交由编排者完成。
