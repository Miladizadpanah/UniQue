package service

import (
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PrometheusService exposes live metrics for the panel and every node in the
// fleet, scrapable at GET /panel/metrics.
//
// The exporter is process-wide: one register, one handler. Node metrics are
// gauges keyed by node name; they start at zero and are updated by each poll.
// CPU/RAM are gauges, traffic counters are counters, online nodes are gauges
// keyed 0/1.
type PrometheusService struct {
	mu sync.Mutex

	// Node gauges
	NodeOnline        *prometheus.GaugeVec
	NodeInbounds      *prometheus.GaugeVec
	NodeAccounts      *prometheus.GaugeVec
	NodeOnlineClients *prometheus.GaugeVec
	NodeLatencyMs     *prometheus.GaugeVec

	// Hub gauges
	PanelUp    *prometheus.GaugeVec
	PanelCores *prometheus.GaugeVec

	// Traffic counters
	PanelTrafficUp   *prometheus.CounterVec
	PanelTrafficDown *prometheus.CounterVec

	// Last observed absolute traffic values, used to diff for Add()
	lastTrafficUp   int64
	lastTrafficDown int64

	// Handler
	Handler gin.HandlerFunc
}

var (
	promInstance *PrometheusService
	promOnce     sync.Once
)

// GetPrometheus returns the process-wide exporter.
func GetPrometheus() *PrometheusService {
	promOnce.Do(func() {
		promInstance = &PrometheusService{}
		promInstance.register()
	})
	return promInstance
}

func (s *PrometheusService) register() {
	namespace := "vpnui"

	// Node gauges, keyed by node name
	s.NodeOnline = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "node",
		Name: "online", Help: "Whether the node answered its last poll",
	}, []string{"node"})
	s.NodeInbounds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "node",
		Name: "inbounds_total", Help: "Inbound count last reported by the node",
	}, []string{"node"})
	s.NodeAccounts = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "node",
		Name: "accounts_total", Help: "Account count last reported by the node",
	}, []string{"node"})
	s.NodeOnlineClients = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "node",
		Name: "online_clients", Help: "Unique online clients last reported by the node",
	}, []string{"node"})
	s.NodeLatencyMs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "node",
		Name: "poll_latency_ms", Help: "Milliseconds taken by the last status poll",
	}, []string{"node"})

	// Hub gauges
	s.PanelUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "panel",
		Name: "up", Help: "Whether the panel is answering",
	}, []string{"panel"})
	s.PanelCores = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "panel",
		Name: "cores_up", Help: "How many of the panel's cores are running",
	}, []string{"panel"})

	// Traffic counters (bytes, cumulative)
	s.PanelTrafficUp = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "panel",
		Name: "traffic_up_total_bytes", Help: "Cumulative upload bytes",
	}, []string{"panel"})
	s.PanelTrafficDown = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "panel",
		Name: "traffic_down_total_bytes", Help: "Cumulative download bytes",
	}, []string{"panel"})

	prometheus.MustRegister(
		s.NodeOnline, s.NodeInbounds, s.NodeAccounts,
		s.NodeOnlineClients, s.NodeLatencyMs,
		s.PanelUp, s.PanelCores,
		s.PanelTrafficUp, s.PanelTrafficDown,
	)

	// Handler: serve the default registry at /metrics
	s.Handler = func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		promhttp.Handler().ServeHTTP(c.Writer, c.Request)
	}
}

// UpdateNodeGauges is called after each successful poll.
func (s *PrometheusService) UpdateNodeGauges(name string, online bool, inbounds, accounts, onlineClients, latencyMs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if online {
		s.NodeOnline.WithLabelValues(name).Set(1)
	} else {
		s.NodeOnline.WithLabelValues(name).Set(0)
	}
	s.NodeInbounds.WithLabelValues(name).Set(float64(inbounds))
	s.NodeAccounts.WithLabelValues(name).Set(float64(accounts))
	s.NodeOnlineClients.WithLabelValues(name).Set(float64(onlineClients))
	s.NodeLatencyMs.WithLabelValues(name).Set(float64(latencyMs))
}

// UpdatePanelGauges refreshes the hub's own gauges on a timer.
// Traffic counters use Add (they are cumulative, not absolute).
func (s *PrometheusService) UpdatePanelGauges(coresUp int64, totalUp, totalDown int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PanelUp.WithLabelValues("panel").Set(1)
	s.PanelCores.WithLabelValues("panel").Set(float64(coresUp))
	// Counters: Add the delta since last call. Since we pass absolute values,
	// we must compute the delta. Keep the previous absolute value to diff.
	s.PanelTrafficUp.WithLabelValues("panel").Add(float64(totalUp - s.lastTrafficUp))
	s.PanelTrafficDown.WithLabelValues("panel").Add(float64(totalDown - s.lastTrafficDown))
	s.lastTrafficUp = totalUp
	s.lastTrafficDown = totalDown
}
