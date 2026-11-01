package service

import (
	"errors"
	"strings"
	"sync"

	gopsCpu "github.com/shirou/gopsutil/v4/cpu"
	gopsHost "github.com/shirou/gopsutil/v4/host"
	gopsNet "github.com/shirou/gopsutil/v4/net"
)

// ---------------------------------------------------------------------------
// Heartbeat metrics: the node-side gauges a node PUSHES to the hub. The hub's
// pull poll still runs, but the push path makes the hub's row live (CPU/RAM/
// uptime/network/Xray state) instead of waiting for the 60s poll cadence.
//
// All readers are nil/zero-safe: a node that predates heartbeats reports
// zeros and the hub renders "no data" rather than a wrong number.
// ---------------------------------------------------------------------------

var (
	hbCpuMu   sync.Mutex
	hbCpuPrev *gopsCpu.TimesStat
)

// sampleCpuOnce returns the host's CPU utilization percentage over the window
// since the PREVIOUS call. Stateless w.r.t. the dashboard's own ServerService
// sampler (they must not share an EMA or the 2s dashboard poll and the 30s
// heartbeat would corrupt each other's windows). The first call after a panel
// boot has no window and returns 0; the next call (>=30s later) reports a
// real figure.
func sampleCpuOnce() (float64, error) {
	times, err := gopsCpu.Times(false)
	if err != nil {
		return 0, err
	}
	if len(times) == 0 {
		return 0, errors.New("no cpu times available")
	}
	cur := times[0]
	hbCpuMu.Lock()
	prev := hbCpuPrev
	hbCpuPrev = &cur
	hbCpuMu.Unlock()
	if prev == nil {
		return 0, nil
	}
	total := cpuDelta(cpuTotal(cur), cpuTotal(*prev))
	if total <= 0 {
		return 0, nil
	}
	idle := cpuDelta(cpuIdle(cur), cpuIdle(*prev))
	busy := total - idle
	if busy < 0 {
		busy = 0
	}
	return float64(busy) / float64(total) * 100, nil
}

// cpuTotal sums the aggregate TimesStat's busy-side fields; cpuIdle its
// idle-side ones. Both are cumulative-since-boot counters, so the caller
// takes deltas against the previous sample.
func cpuTotal(s gopsCpu.TimesStat) float64 {
	return s.User + s.System + s.Idle + s.Iowait + s.Irq +
		s.Softirq + s.Steal + s.Guest + s.GuestNice
}

func cpuIdle(s gopsCpu.TimesStat) float64 {
	return s.Idle + s.Iowait
}

// cpuDelta clamps a cumulative-counter delta at zero: a backwards read
// (counter wrap, a VM migrating) has no honest rate, and reporting it would
// print a negative utilization the hub's UI would render as a red flag.
func cpuDelta(cur, prev float64) float64 {
	d := cur - prev
	if d < 0 {
		return 0
	}
	return d
}

// netIOCountersAggregate returns the host's cumulative network send/receive
// bytes summed over every REAL interface. Loopback and docker/veth-family
// virtuals are excluded so the aggregate is the machine's external traffic,
// which is what a hub's "tunnel speed" gauge wants to see. The ABSOLUTE
// counters are returned (not rates): both the hub's row and this node's next
// beat derive the rate against their own stored baselines, so a panel restart
// costs one silent beat, never a wrong number.
func netIOCountersAggregate() (out struct{ Sent, Recv uint64 }, err error) {
	stats, err := gopsNet.IOCounters(true)
	if err != nil {
		return out, err
	}
	for _, s := range stats {
		if isNonRealInterface(s.Name) {
			continue
		}
		out.Sent += s.BytesSent
		out.Recv += s.BytesRecv
	}
	return out, nil
}

// isNonRealInterface mirrors the dashboard's own filter closely enough for an
// aggregate: anything that is not real external traffic is skipped.
func isNonRealInterface(name string) bool {
	switch name {
	case "lo", "lo0", "null0":
		return true
	}
	prefixes := []string{"docker", "veth", "br-", "virbr", "tun", "tap", "wg", "gre", "ipsec", "ifb", "dummy", "cali", "flannel", "kube"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// hostUptime returns the machine's uptime in seconds, or 0 when the platform
// does not expose it. Deliberately the HOST's uptime, not the panel's: a
// panel restart must not read as a node reboot on a hub's dashboard.
func hostUptime() uint64 {
	up, err := gopsHost.Uptime()
	if err != nil {
		return 0
	}
	return up
}
