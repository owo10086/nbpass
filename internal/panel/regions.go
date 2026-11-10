package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// Region 将地区展示与中国大陆归属分开，避免用显示文字推断检测点资格。
type Region struct {
	Name          string `json:"name"`
	MainlandChina bool   `json:"mainland_china"`
}

const regionMigration = `CREATE TABLE node_regions (
	node_id TEXT PRIMARY KEY REFERENCES nodes(id),
	public_ipv4 TEXT NOT NULL DEFAULT '',
	auto_name TEXT NOT NULL DEFAULT '未知',
	auto_mainland INTEGER NOT NULL DEFAULT 0 CHECK (auto_mainland IN (0, 1)),
	override_name TEXT,
	override_mainland INTEGER NOT NULL DEFAULT 0 CHECK (override_mainland IN (0, 1)),
	checker_disabled INTEGER NOT NULL DEFAULT 0 CHECK (checker_disabled IN (0, 1))
);`

type nodeRegionView struct {
	PublicIPv4      string `json:"public_ipv4"`
	ConfirmedIPv4   string `json:"confirmed_ipv4"`
	Region          Region `json:"region"`
	AutomaticRegion Region `json:"automatic_region"`
	RegionSource    string `json:"region_source"`
	DomesticChecker bool   `json:"domestic_checker"`
	CheckerDisabled bool   `json:"checker_disabled"`
	CheckerLabel    string `json:"checker_label"`
}

func (n *nodeRegionView) resolve(override sql.NullString, mainland bool) {
	n.Region, n.RegionSource = n.AutomaticRegion, "auto"
	if override.Valid {
		n.Region, n.RegionSource = Region{Name: override.String, MainlandChina: mainland}, "manual"
	}
	n.DomesticChecker = n.Region.MainlandChina && !n.CheckerDisabled
	n.CheckerLabel = "不承担"
	if n.DomesticChecker {
		n.CheckerLabel = "国内检测点"
	} else if n.CheckerDisabled {
		n.CheckerLabel = "已关闭"
	}
}

// 调用者已在 controlMu 内校验了控制会话；忽略旧节点未上报的字段。
func (p *Panel) recordNodeIP(ctx context.Context, id, ip string) error {
	if ip == "" {
		return nil
	}
	var previous, name string
	err := p.db.QueryRowContext(ctx, "SELECT public_ipv4, auto_name FROM node_regions WHERE node_id = ?", id).Scan(&previous, &name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous == ip && name != "未知" {
		return nil
	}
	region := Region{Name: "未知"}
	if p.deps.RegionLookup != nil {
		if result, err := p.deps.RegionLookup(ctx, ip); err == nil && result.Name != "" {
			region = result
		}
	}
	_, err = p.db.ExecContext(ctx, `INSERT INTO node_regions(node_id, public_ipv4, auto_name, auto_mainland,domestic_since)
		VALUES(?, ?, ?, ?,?) ON CONFLICT(node_id) DO UPDATE SET public_ipv4 = excluded.public_ipv4,
		auto_name = excluded.auto_name, auto_mainland = excluded.auto_mainland,
 domestic_since=CASE WHEN node_regions.public_ipv4<>excluded.public_ipv4 THEN excluded.domestic_since ELSE node_regions.domestic_since END`, id, ip, region.Name, region.MainlandChina, p.deps.Clock.Now().UnixNano())
	if err == nil {
		_ = p.notifyNodeAddressDrift(ctx)
	}
	return err
}

// JSON null 清除人工覆盖；开关独立保存，恢复自动地区不会重开检测点角色。
func (p *Panel) setNodeRegion(w http.ResponseWriter, r *http.Request) {
	var region *Region
	if !decode(w, r, &region) {
		return
	}
	if region != nil {
		region.Name = strings.TrimSpace(region.Name)
		if region.Name == "" || len(region.Name) > 200 {
			problem(w, 400, "地区名称须为 1–200 字节")
			return
		}
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	var name any
	mainland := false
	if region != nil {
		name, mainland = region.Name, region.MainlandChina
	}
	result, err := p.db.ExecContext(r.Context(), `INSERT INTO node_regions(node_id, override_name, override_mainland)
		SELECT id, ?, ? FROM nodes WHERE id = ? ON CONFLICT(node_id) DO UPDATE SET
		override_name = excluded.override_name, override_mainland = excluded.override_mainland`, name, mainland, r.PathValue("id"))
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if count == 0 {
		problem(w, 404, "节点不存在")
		return
	}
	respond(w, 204, nil)
}

func (p *Panel) setNodeChecker(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		problem(w, 400, "请指定是否允许承担国内检测点角色")
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	result, err := p.db.ExecContext(r.Context(), `INSERT INTO node_regions(node_id, checker_disabled)
		SELECT id, ? FROM nodes WHERE id = ? ON CONFLICT(node_id) DO UPDATE SET
		checker_disabled = excluded.checker_disabled`, !*body.Enabled, r.PathValue("id"))
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if count == 0 {
		problem(w, 404, "节点不存在")
		return
	}
	respond(w, 204, nil)
}
