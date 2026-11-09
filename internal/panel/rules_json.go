package panel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"nbpass/internal/forwarding"
)

const rulesFormatVersion = 1

// 只导出规则定义，不从含运行状态、授权及凭据的视图或快照序列化。
func (p *Panel) exportRules(w http.ResponseWriter, r *http.Request) {
	rules, err := loadRules(r.Context(), p.db)
	if err != nil {
		problem(w, 503, "无法读取规则")
		return
	}
	if ids := r.URL.Query()["id"]; len(ids) > 0 {
		selected := make(map[string]bool, len(ids))
		for _, id := range ids {
			selected[id] = true
		}
		filtered := []forwarding.Rule{}
		for _, rule := range rules {
			if selected[rule.ID] {
				filtered = append(filtered, rule)
				delete(selected, rule.ID)
			}
		}
		if len(selected) > 0 {
			problem(w, 404, "选中的规则不存在，请刷新后重试")
			return
		}
		rules = filtered
	}
	w.Header().Set("Content-Disposition", `attachment; filename="nbpass-rules.json"`)
	respond(w, 200, struct {
		FormatVersion int               `json:"format_version"`
		Rules         []forwarding.Rule `json:"rules"`
	}{rulesFormatVersion, rules})
}

type ruleImportRequest struct {
	Mode     string `json:"mode"`
	Document struct {
		FormatVersion int               `json:"format_version"`
		Rules         []json.RawMessage `json:"rules"`
	} `json:"document"`
}

type ruleImportRow struct {
	ConflictCount int                            `json:"conflict_count,omitempty"`
	Index         int                            `json:"index"`
	ID            string                         `json:"id"`
	Name          string                         `json:"name"`
	Action        string                         `json:"action"`
	Errors        []string                       `json:"errors"`
	Listeners     []forwarding.ListenerReference `json:"listeners,omitempty"`
}

type ruleImportResult struct {
	Valid   bool            `json:"valid"`
	Mode    string          `json:"mode"`
	Results []ruleImportRow `json:"results"`
	Error   string          `json:"error,omitempty"`
}

var errRuleImportPreview = errors.New("规则导入预检完成，回滚暂存事务")
var errRuleImportInvalid = errors.New("规则导入预检未通过")

func (p *Panel) importRules(w http.ResponseWriter, r *http.Request) {
	var body ruleImportRequest
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		problem(w, 415, "请求体必须使用 JSON")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data)), 0) != nil {
		problem(w, 400, "导入 JSON 无效、键重复、嵌套过深或超过 1 MiB")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		problem(w, 400, "导入格式无效、含未知字段或超过 1 MiB")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, 400, "只允许一个 JSON 对象")
		return
	}
	if body.Mode != "create" && body.Mode != "overwrite" {
		problem(w, 400, "须明确选择新增（create）或覆盖（overwrite）")
		return
	}
	if body.Document.FormatVersion != rulesFormatVersion || len(body.Document.Rules) < 1 || len(body.Document.Rules) > 500 {
		problem(w, 400, "仅支持格式版本 1，每批须包含 1–500 条规则")
		return
	}
	result := ruleImportResult{Mode: body.Mode, Results: make([]ruleImportRow, len(body.Document.Rules))}
	err = p.commitForwarding(r.Context(), func(tx *sql.Tx) error {
		rules, err := p.preflightRuleImport(r.Context(), tx, body, &result)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if err := storeRule(r.Context(), tx, rule, body.Mode == "create"); err != nil {
				return err
			}
		}
		if r.URL.Path == "/api/rules/import/preflight" {
			// 编译也参与预检（含快照大小和隧道授权）；事务回滚且不通知节点。
			if _, err := p.compileSnapshots(r.Context(), tx); err != nil {
				return err
			}
			return errRuleImportPreview
		}
		return nil
	})
	if errors.Is(err, errRuleImportInvalid) {
		result.Error = "整体预检未通过，未写入任何规则"
		respond(w, 400, result)
		return
	}
	if err != nil && !errors.Is(err, errRuleImportPreview) {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法编译或保存导入配置，未写入任何规则")
		}
		return
	}
	respond(w, 200, result)
}

// encoding/json 默认接受重复键且后值覆盖前值，导入文件须避免这种歧义。
func uniqueJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON 嵌套过深")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				// 与 encoding/json 的字段匹配保持同一 Unicode 简单折叠规则。
				name = strings.Map(func(r rune) rune {
					for {
						next := unicode.SimpleFold(r)
						if next <= r {
							return next
						}
						r = next
					}
				}, name)
				if !ok || seen[name] {
					return errors.New("JSON 键重复")
				}
				seen[name] = true
				if err := uniqueJSONKeys(decoder, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := uniqueJSONKeys(decoder, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("JSON 格式无效")
		}
		_, err = decoder.Token()
	}
	return err
}

func decodeImportedRule(data []byte, rule *forwarding.Rule) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(rule); err != nil {
		return errors.New("规则字段类型无效或含未知字段")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return errors.New("规则必须为 JSON 对象")
	}
	for _, field := range []string{"name", "entry_group_id", "kind", "listen_port", "target_address", "target_family", "target_port", "allow_private", "enabled"} {
		if len(fields[field]) == 0 || bytes.Equal(fields[field], []byte("null")) {
			return errors.New("缺少必填字段或字段为 null：" + field)
		}
	}
	return rule.Validate()
}

func (p *Panel) preflightRuleImport(ctx context.Context, tx *sql.Tx, body ruleImportRequest, result *ruleImportResult) ([]forwarding.Rule, error) {
	existing, err := loadRules(ctx, tx)
	if err != nil {
		return nil, err
	}
	byID := map[string]forwarding.Rule{}
	for _, rule := range existing {
		byID[rule.ID] = rule
	}
	seen := map[string]int{}
	imported := make([]forwarding.Rule, len(body.Document.Rules))
	for i, raw := range body.Document.Rules {
		row := &result.Results[i]
		*row = ruleImportRow{Index: i + 1, Action: body.Mode, Errors: []string{}}
		rule := &imported[i]
		if err := decodeImportedRule(raw, rule); err != nil {
			row.Errors = append(row.Errors, err.Error())
		}
		row.Name, row.ID = rule.Name, rule.ID
		if rule.ID != "" {
			if first, ok := seen[rule.ID]; ok {
				row.Errors = append(row.Errors, "批内规则 ID 重复")
				result.Results[first].Errors = append(result.Results[first].Errors, "批内规则 ID 重复")
			}
			seen[rule.ID] = i
		}
		if body.Mode == "overwrite" {
			if _, ok := byID[rule.ID]; !ok {
				row.Errors = append(row.Errors, "覆盖须提供已存在的规则 ID，不会自动新增")
			}
		} else {
			rule.ID, _, err = p.randomCredential()
			if err != nil {
				return nil, err
			}
			row.ID = rule.ID
		}
		for _, ref := range []struct{ table, id, label string }{{"entry_groups", rule.EntryGroupID, "入口组"}, {"exit_groups", rule.ExitGroupID, "出口组"}} {
			if ref.table == "exit_groups" && rule.Kind != forwarding.Tunnel {
				continue
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+ref.table+" WHERE id=?", ref.id).Scan(&count); err != nil {
				return nil, err
			}
			if count == 0 {
				row.Errors = append(row.Errors, ref.label+"不存在")
			}
		}
	}
	// 在内存中构造整批最终状态，允许合法的端口交换；此时尚未写业务数据。
	indices := map[string]int{}
	for i, rule := range imported {
		if body.Mode == "overwrite" {
			delete(byID, rule.ID)
		}
		if len(result.Results[i].Errors) == 0 {
			byID[rule.ID], indices[rule.ID] = rule, i
		}
	}
	members := map[string][]string{}
	rows, err := tx.QueryContext(ctx, "SELECT group_id,node_id FROM entry_group_members ORDER BY group_id,position")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var group, node string
		if err := rows.Scan(&group, &node); err != nil {
			rows.Close()
			return nil, err
		}
		members[group] = append(members[group], node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	effective := make([]forwarding.Rule, 0, len(byID))
	for _, rule := range byID {
		effective = append(effective, rule)
	}
	sort.Slice(effective, func(i, j int) bool { return effective[i].ID < effective[j].ID })
	failures, err := p.configurationErrors(ctx, tx, effective, members, true)
	if err != nil {
		return nil, err
	}
	for _, failure := range failures {
		for _, listener := range failure.Listeners {
			if i, ok := indices[listener.RuleID]; ok {
				row := &result.Results[i]
				row.ConflictCount++
				if len(row.Errors) > 0 {
					continue
				}
				// 每行只保留本行端点和一条相关证据，避免批量冲突产生平方大小响应。
				row.Listeners = []forwarding.ListenerReference{listener}
				for _, other := range failure.Listeners {
					if other != listener {
						row.Listeners = append(row.Listeners, other)
						break
					}
				}
				label := "监听端口冲突"
				if failure.Code == "forwarding_loop" {
					label = "配置会形成转发循环"
				}
				message := label + "：" + listener.String()
				if len(row.Listeners) > 1 {
					message += "与" + row.Listeners[1].String()
				}
				message += fmt.Sprintf("（本处共 %d 处相关监听）", len(failure.Listeners))
				row.Errors = append(row.Errors, message)
			}
		}
	}
	for _, row := range result.Results {
		if len(row.Errors) > 0 {
			return nil, errRuleImportInvalid
		}
	}
	result.Valid = true
	return imported, nil
}

func storeRule(ctx context.Context, tx *sql.Tx, rule forwarding.Rule, creating bool) error {
	args := []any{rule.Name, rule.EntryGroupID, rule.Kind, rule.ListenPort, rule.TargetAddress, rule.TargetFamily, rule.TargetPort, rule.AllowPrivate, rule.Enabled, rule.ExitGroupID, rule.ID}
	if creating {
		_, err := tx.ExecContext(ctx, `INSERT INTO forward_rules(name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id,id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, args...)
		return err
	}
	return missingResult(tx.ExecContext(ctx, `UPDATE forward_rules SET name=?,entry_group_id=?,kind=?,listen_port=?,target_address=?,target_family=?,target_port=?,allow_private=?,enabled=?,exit_group_id=? WHERE id=?`, args...))
}
