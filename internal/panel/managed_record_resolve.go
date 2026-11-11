package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"nbpass/internal/dnsprovider"
	"nbpass/internal/publicip"
)

// recordTarget 是三种归属共用的绑定请求：按主机记录名绑定，没有就新建、有一条就接管。
// 只给 record_id 时按记录 ID 直接接管（下拉直选）；多条候选或类型冲突时由管理员再次指定。
type recordTarget struct {
	AccountID       string `json:"account_id"`
	Domain          string `json:"domain"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	RecordID        string `json:"record_id"`
	LineID          string `json:"line_id"`
	ConvertRecordID string `json:"convert_record_id"`
	Create          bool   `json:"create"`
}

// recordTargetRules 由各归属给出：line 非空时只接管或新建该线路；initial 给新建与改类型时的初始地址；
// cname 非空表示允许 CNAME，并给出新建与改类型时的初始目标。
type recordTargetRules struct {
	line    string
	initial func(context.Context) (string, error)
	cname   func(context.Context) (string, error)
}

type resolvedRecord struct {
	domain    string
	kind      string
	result    dnsprovider.ReadResult
	warnings  []string
	created   bool
	converted bool
}

// recordChoice 是需要管理员再次选择时的 409 应答。
type recordChoice struct {
	Message    string               `json:"error"`
	Candidates []dnsprovider.Record `json:"candidates,omitempty"`
	Conflicts  []dnsprovider.Record `json:"conflicts,omitempty"`
}

// 新建记录固定默认线路；TTL 取 DNSPod 免费套餐也接受的 600，首次发布时按发布 TTL 改写。
const (
	createdLine   = "默认"
	createdLineID = "0"
	createdTTL    = 600
)

var recordNamePattern = regexp.MustCompile(`^(\*|(\*\.)?[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*)$`)

// normalizeRecordName 接受 "@"、"www" 或带主域名的完整写法，返回 DNSPod 的主机记录名。
func normalizeRecordName(name, domain string) (string, error) {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if n == "@" || n == domain {
		return "@", nil
	}
	n = strings.TrimSuffix(n, "."+domain)
	if n == "" || len(n) > 200 || !recordNamePattern.MatchString(n) {
		return "", errors.New("主机记录名无效")
	}
	return n, nil
}

// resolveRecordTarget 在加锁和开启事务之前完成服务商侧的读取、新建或改类型；失败时已写出应答。
// 新建和改类型都是暂停状态，由首次发布或加回步骤启用；面板从不删除记录。
func (p *Panel) resolveRecordTarget(w http.ResponseWriter, r *http.Request, body recordTarget, rules recordTargetRules) (resolvedRecord, bool) {
	var out resolvedRecord
	domain, err := normalizeDNSDomain(body.Domain)
	if err != nil || body.AccountID == "" || len(body.RecordID) > 100 || len(body.ConvertRecordID) > 100 || len(body.LineID) > 100 || len(body.Name) > 253 {
		problem(w, 400, "请指定 DNSPod 账号、主域名和主机记录名")
		return out, false
	}
	out.domain = domain
	if body.Type == "" {
		body.Type = "A"
	}
	if body.Type != "A" && (body.Type != "CNAME" || rules.cname == nil) {
		problem(w, 400, "该绑定只支持 A 记录；CNAME 只用于主备入口组")
		return out, false
	}
	out.kind = body.Type
	credentials, err := p.dnsCredential(r.Context(), body.AccountID)
	if err != nil {
		problem(w, 400, "DNSPod 账号未配置或凭据不可用")
		return out, false
	}
	if p.deps.DNSProvider == nil {
		problem(w, 503, "DNS 服务商不可用")
		return out, false
	}
	auth := credentials.provider()
	ctx, cancel := p.dnsContext(r.Context())
	defer cancel()
	if body.Name == "" {
		if body.RecordID == "" {
			problem(w, 400, "请填写主机记录名")
			return out, false
		}
		out.result, err = p.deps.DNSProvider.ReadRecord(ctx, auth, dnsprovider.Key{Domain: domain, RecordID: body.RecordID})
		if err != nil {
			problem(w, 502, "DNSPod 读取失败，请核对账号和记录")
			return out, false
		}
		return p.checkResolved(w, out, rules)
	}
	name, err := normalizeRecordName(body.Name, domain)
	if err != nil {
		problem(w, 400, err.Error())
		return out, false
	}
	records, err := p.deps.DNSProvider.FindRecords(ctx, auth, domain, name)
	if err != nil {
		browseProblem(w, err)
		return out, false
	}
	inLine := func(rec dnsprovider.Record) bool { return rules.line == "" || rec.LineID == rules.line }
	var same, conflicts []dnsprovider.Record
	for _, rec := range records {
		// CNAME 与同名的其他记录互斥：选 A 时同名 CNAME 冲突，选 CNAME 时同名其他记录都冲突。
		switch {
		case rec.Type == body.Type && inLine(rec):
			same = append(same, rec)
		case rec.Type == "CNAME" || body.Type == "CNAME" && rec.Type != body.Type:
			conflicts = append(conflicts, rec)
		}
	}
	find := func(list []dnsprovider.Record, id string) (dnsprovider.Record, bool) {
		i := slices.IndexFunc(list, func(rec dnsprovider.Record) bool { return rec.ID == id })
		if i < 0 {
			return dnsprovider.Record{}, false
		}
		return list[i], true
	}
	switch {
	case body.RecordID != "":
		rec, found := find(same, body.RecordID)
		if !found {
			problem(w, 400, "所选记录不是该名字下可接管的 "+body.Type+" 记录")
			return out, false
		}
		out.result = dnsprovider.ReadResult{Record: rec, Related: records}
		return p.checkResolved(w, out, rules)
	case body.ConvertRecordID != "":
		rec, found := find(conflicts, body.ConvertRecordID)
		if !found {
			problem(w, 400, "所选记录不是该名字下的冲突记录")
			return out, false
		}
		return p.convertRecord(w, r, out, auth, rec, records, body.Type, rules)
	case body.Create:
		return p.createRecord(w, r, out, auth, name, records, body.Type, rules)
	case len(same) == 1:
		out.result = dnsprovider.ReadResult{Record: same[0], Related: records}
		return p.checkResolved(w, out, rules)
	case len(same) > 1:
		respond(w, 409, recordChoice{Message: "同名 " + body.Type + " 记录有多条，请选择接管哪一条，或新建一条", Candidates: same})
		return out, false
	case len(conflicts) > 0:
		respond(w, 409, recordChoice{Message: "该名字已有其他类型的记录，确认后改成 " + body.Type + " 再接管", Conflicts: conflicts})
		return out, false
	}
	return p.createRecord(w, r, out, auth, name, records, body.Type, rules)
}

func (p *Panel) checkResolved(w http.ResponseWriter, out resolvedRecord, rules recordTargetRules) (resolvedRecord, bool) {
	rec := out.result.Record
	if rec.Type != out.kind {
		problem(w, 400, "所选记录不是 "+out.kind+" 记录")
		return out, false
	}
	if rules.line != "" && rec.LineID != rules.line {
		problem(w, 400, "成员记录须与组域名同名、同线路")
		return out, false
	}
	var err error
	out.warnings, err = recordWarnings(out.result, rec.ID, rec.LineID)
	if err != nil {
		problem(w, 400, err.Error())
		return out, false
	}
	return out, true
}

func (p *Panel) initialValue(w http.ResponseWriter, r *http.Request, rules recordTargetRules, kind string) (string, bool) {
	if kind == "CNAME" {
		value, err := rules.cname(r.Context())
		if err != nil {
			problem(w, 503, "无法读取节点记录")
			return "", false
		}
		if value == "" {
			problem(w, 409, "首选入口还没有绑定节点记录，CNAME 无处可指；请先在节点的“域名与地址”里绑定节点记录")
			return "", false
		}
		return value, true
	}
	value, err := rules.initial(r.Context())
	if _, parseErr := publicip.Parse(value); err == nil && value != "" && parseErr != nil {
		value = ""
	}
	if err != nil {
		problem(w, 503, "无法读取节点地址")
		return "", false
	}
	if value == "" {
		problem(w, 409, "节点还没有公网地址，无法新建或改写记录；等节点上线后重试，或先在 DNSPod 建好记录再接管")
		return "", false
	}
	return value, true
}

// createRecord 新建暂停的记录。结果未知时按名字读回核对：只认领本次之前不存在、且与请求一致的唯一一条，
// 否则让管理员刷新后重新绑定，不自动再建，避免重复记录。
func (p *Panel) createRecord(w http.ResponseWriter, r *http.Request, out resolvedRecord, auth dnsprovider.Credentials, name string, before []dnsprovider.Record, kind string, rules recordTargetRules) (resolvedRecord, bool) {
	if rules.line != "" && rules.line != createdLineID {
		problem(w, 400, "组内成员记录不在默认线路，面板只能在默认线路新建；请先在 DNSPod 建好同线路记录再接管")
		return out, false
	}
	value, ok := p.initialValue(w, r, rules, kind)
	if !ok {
		return out, false
	}
	rec := dnsprovider.Record{ID: "new", Name: name, Type: kind, Line: createdLine, LineID: createdLineID, Value: value, TTL: createdTTL}
	// 先用拟建记录做同名校验，避免建出无法绑定的记录。
	if _, err := recordWarnings(dnsprovider.ReadResult{Record: rec, Related: before}, rec.ID, rec.LineID); err != nil {
		problem(w, 400, err.Error())
		return out, false
	}
	rec.ID = ""
	ctx, cancel := p.dnsContext(r.Context())
	id, err := p.deps.DNSProvider.CreateRecord(ctx, auth, out.domain, rec)
	cancel()
	var rejected *dnsprovider.RejectionError
	if errors.As(err, &rejected) {
		problem(w, 400, "DNSPod 拒绝新建记录（代码 "+rejected.Code+"），请核对该名字下的已有记录")
		return out, false
	}
	if err != nil {
		ctx, cancel = p.dnsContext(r.Context())
		after, readErr := p.deps.DNSProvider.FindRecords(ctx, auth, out.domain, name)
		cancel()
		var matches []dnsprovider.Record
		for _, candidate := range after {
			if !slices.ContainsFunc(before, func(old dnsprovider.Record) bool { return old.ID == candidate.ID }) && candidate.Type == kind && candidate.LineID == rec.LineID && candidate.Value == rec.Value {
				matches = append(matches, candidate)
			}
		}
		if readErr != nil || len(matches) != 1 {
			problem(w, 502, "DNSPod 新建结果未确认，请刷新后重新绑定；面板不会自动再建")
			return out, false
		}
		id = matches[0].ID
	}
	rec.ID = id
	out.created = true
	out.result = dnsprovider.ReadResult{Record: rec, Related: append(slices.Clone(before), rec)}
	return p.checkResolved(w, out, rules)
}

// convertRecord 把管理员确认的那条同名记录改成所选类型并暂停；记录 ID 不变，原值记入提示。
func (p *Panel) convertRecord(w http.ResponseWriter, r *http.Request, out resolvedRecord, auth dnsprovider.Credentials, old dnsprovider.Record, records []dnsprovider.Record, kind string, rules recordTargetRules) (resolvedRecord, bool) {
	if rules.line != "" && old.LineID != rules.line {
		problem(w, 400, "成员记录须与组域名同名、同线路")
		return out, false
	}
	value, ok := p.initialValue(w, r, rules, kind)
	if !ok {
		return out, false
	}
	rec := old
	rec.Type, rec.Value, rec.Enabled = kind, value, false
	related := slices.DeleteFunc(slices.Clone(records), func(other dnsprovider.Record) bool { return other.ID == old.ID })
	related = append(related, rec)
	if _, err := recordWarnings(dnsprovider.ReadResult{Record: rec, Related: related}, rec.ID, rec.LineID); err != nil {
		problem(w, 400, err.Error())
		return out, false
	}
	ctx, cancel := p.dnsContext(r.Context())
	err := p.deps.DNSProvider.WriteRecord(ctx, auth, dnsprovider.Key{Domain: out.domain, RecordID: old.ID}, rec)
	cancel()
	var rejected *dnsprovider.RejectionError
	if errors.As(err, &rejected) {
		problem(w, 400, "DNSPod 拒绝改类型（代码 "+rejected.Code+"）")
		return out, false
	}
	if err != nil {
		problem(w, 502, "DNSPod 改类型结果未确认，请刷新后重新绑定")
		return out, false
	}
	out.converted = true
	out.result = dnsprovider.ReadResult{Record: rec, Related: related}
	out, ok = p.checkResolved(w, out, rules)
	if ok {
		out.warnings = append(out.warnings, "原 "+old.Type+" 记录（"+old.Value+"）已改为 "+kind+" 并暂停，首次发布或加回后启用")
	}
	return out, ok
}

// nodeInitialIPv4 给新建或改类型的记录一个占位地址：优先面板确认的地址，没有时用节点上报的公网地址。
// 记录是暂停的，占位地址不会对外解析；首次发布或加回前由发布流程写入确认地址。
func (p *Panel) nodeInitialIPv4(nodeID string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var address string
		err := p.db.QueryRowContext(ctx, "SELECT coalesce((SELECT coalesce(nullif(confirmed_ipv4,''),public_ipv4) FROM node_regions WHERE node_id=?),'')", nodeID).Scan(&address)
		return address, err
	}
}

// preferredCNAMETarget 取主备组首选入口的节点记录域名；首选入口没有节点记录时返回空。
func (p *Panel) preferredCNAMETarget(groupID string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var node string
		err := p.db.QueryRowContext(ctx, "SELECT node_id FROM entry_group_members WHERE group_id=? ORDER BY position LIMIT 1", groupID).Scan(&node)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		target, err := cnameTarget(ctx, p.db, node, "", false)
		var missing *forwardingError
		if errors.As(err, &missing) {
			return "", nil
		}
		return target, err
	}
}

// preferredInitialIPv4 取主备组首选入口的占位地址，规则同 nodeInitialIPv4。
func (p *Panel) preferredInitialIPv4(groupID string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var address string
		err := p.db.QueryRowContext(ctx, "SELECT coalesce(nullif(r.confirmed_ipv4,''),r.public_ipv4,'') FROM entry_group_members m LEFT JOIN node_regions r ON r.node_id=m.node_id WHERE m.group_id=? ORDER BY m.position LIMIT 1", groupID).Scan(&address)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return address, err
	}
}
