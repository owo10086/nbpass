## Agent skills

### Issue tracker

Issues and specs live as local markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`), recorded as a `Status:` line in each issue file. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

### 本地 Git 自动提交

- 每次完成一项已获用户授权的任务调整，并完成相应验证后，自动提交该任务的改动到本地 Git，无需再次询问提交许可。
- 仅提交本次任务相关改动；工作区已有的其他改动保持原状。无文件改动时不创建空提交。
- 提交信息使用中文；代码改动较大时按功能拆分提交，同一轮任务的文档改动在核对完成后统一提交一次。
- 自动提交仅限本地；推送远程需用户明确授权。自动提交规则不改变实施前需获用户明确授权的要求。
