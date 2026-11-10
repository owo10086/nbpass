package clusterharness

import "nbpass/internal/clusterharness/fakedns"

// DNSPod 保持测试台原有接缝；独立的假外部服务也供面板底座边界测试使用。
type DNSPod = fakedns.DNSPod

func NewDNSPod() *DNSPod { return fakedns.NewDNSPod() }
