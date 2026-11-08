// Package ipregion 将内嵌 ip2region IPv4 离线库适配为面板地区查询依赖。
package ipregion

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"

	"nbpass/internal/panel"
)

// 来源固定为 lionsoul2014/ip2region 的提交 6a276cc7c4f9；许可见 LICENSE.ip2region。
//
//go:embed ip2region_v4.xdb
var database []byte

func New() (func(context.Context, string) (panel.Region, error), error) {
	if _, err := xdb.NewWithBuffer(xdb.IPv4, database); err != nil {
		return nil, err
	}
	return func(ctx context.Context, ip string) (panel.Region, error) {
		if err := ctx.Err(); err != nil {
			return panel.Region{}, err
		}
		// 共享只读库字节，每次查询独立 searcher，不共享库内的可变 IO 计数。
		searcher, err := xdb.NewWithBuffer(xdb.IPv4, database)
		if err != nil {
			return panel.Region{}, err
		}
		raw, err := searcher.Search(ip)
		if err != nil {
			return panel.Region{}, err
		}
		fields := strings.Split(raw, "|")
		// 固定版本库格式：国家 | 省份 | 城市 | 运营商 | ISO 国家码。
		if len(fields) != 5 || fields[0] == "" || fields[0] == "0" {
			return panel.Region{}, errors.New("ip2region 未识别地区")
		}
		parts := []string{fields[0]}
		for _, part := range fields[1:3] {
			if part != "" && part != "0" {
				parts = append(parts, part)
			}
		}
		mainland := fields[4] == "CN"
		// 同时防御将港澳台写作中国省级地区的库格式。
		for _, part := range fields[:3] {
			if strings.Contains(part, "香港") || strings.Contains(part, "澳门") || strings.Contains(part, "台湾") {
				mainland = false
			}
		}
		return panel.Region{Name: strings.Join(parts, "·"), MainlandChina: mainland}, nil
	}, nil
}
