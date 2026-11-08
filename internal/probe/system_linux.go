//go:build linux

package probe

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"nbpass/internal/timing"
)

type systemCollector struct {
	clock             timing.Clock
	cpuTotal, cpuIdle uint64
	cpuReady          bool
	network           map[string][2]uint64
	networkAt         time.Time
}

func NewSystem(clock timing.Clock) Collector { return &systemCollector{clock: clock} }

func (s *systemCollector) Collect(ctx context.Context) (Values, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v := Values{}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		fields := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
		var total, idle uint64
		valid := len(fields) >= 5 && fields[0] == "cpu"
		// guest/guest_nice 已经计入 user/nice，不重复累计。
		for i := 1; i < len(fields) && i <= 8; i++ {
			n, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				valid = false
				break
			}
			total += n
			if i == 4 || i == 5 {
				idle += n
			}
		}
		if valid {
			if s.cpuReady && total > s.cpuTotal && idle >= s.cpuIdle && idle-s.cpuIdle <= total-s.cpuTotal {
				v["cpu_percent"] = 100 * (1 - float64(idle-s.cpuIdle)/float64(total-s.cpuTotal))
			}
			s.cpuTotal, s.cpuIdle, s.cpuReady = total, idle, true
		} else {
			s.cpuReady = false
		}
	} else {
		s.cpuReady = false
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		memory := map[string]uint64{}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if n, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					memory[strings.TrimSuffix(fields[0], ":")] = n * 1024
				}
			}
		}
		total, totalOK := memory["MemTotal"]
		available, availableOK := memory["MemAvailable"]
		if totalOK && availableOK && total > 0 && available <= total {
			v["memory_total_bytes"] = float64(total)
			v["memory_used_bytes"] = float64(total - available)
		}
	}
	var disk unix.Statfs_t
	if err := unix.Statfs("/", &disk); err == nil && disk.Blocks >= disk.Bfree {
		v["disk_total_bytes"] = float64(disk.Blocks) * float64(disk.Bsize)
		v["disk_used_bytes"] = float64(disk.Blocks-disk.Bfree) * float64(disk.Bsize)
	}
	now := s.clock.Now()
	if data, err := os.ReadFile("/proc/net/dev"); err == nil {
		current := map[string][2]uint64{}
		for _, line := range strings.Split(string(data), "\n") {
			name, rest, ok := strings.Cut(line, ":")
			name = strings.TrimSpace(name)
			if !ok || name == "lo" {
				continue
			}
			fields := strings.Fields(rest)
			if len(fields) < 16 {
				continue
			}
			rx, e1 := strconv.ParseUint(fields[0], 10, 64)
			tx, e2 := strconv.ParseUint(fields[8], 10, 64)
			if e1 == nil && e2 == nil {
				current[name] = [2]uint64{rx, tx}
			}
		}
		if s.network != nil && now.After(s.networkAt) {
			var rx, tx uint64
			valid := true
			for name, counters := range current {
				if old, ok := s.network[name]; ok {
					if counters[0] < old[0] || counters[1] < old[1] {
						valid = false
						break
					}
					rx += counters[0] - old[0]
					tx += counters[1] - old[1]
				}
			}
			if valid {
				seconds := now.Sub(s.networkAt).Seconds()
				v["network_receive_bytes_per_second"] = float64(rx) / seconds
				v["network_transmit_bytes_per_second"] = float64(tx) / seconds
			}
		}
		s.network, s.networkAt = current, now
	} else {
		s.network = nil
	}
	var tcp uint64
	valid := true
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) && strings.HasSuffix(path, "tcp6") {
			continue
		}
		if err != nil {
			valid = false
			break
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[3] == "01" {
				tcp++
			}
		}
	}
	if valid {
		v["system_tcp_connections"] = float64(tcp)
	}
	return v, nil
}
