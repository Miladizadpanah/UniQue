package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// WebhookService manages webhook notifications for panel events.
type WebhookService struct{}

// WebhookEvent is a notification sent to configured webhook URLs.
type WebhookEvent struct {
	// Type identifies what happened: user.created, user.deleted, traffic.limit_reached, node.offline, etc.
	Type string `json:"type"`
	// Source identifies the panel component: node.<id>, client.<email>, etc.
	Source string `json:"source"`
	// Data carries the event-specific payload.
	Data map[string]any `json:"data"`
	// Timestamp is Unix milliseconds.
	Timestamp int64 `json:"timestamp"`
}

// NewWebhookEvent builds a webhook event.
func NewWebhookEvent(eventType, source string, data map[string]any) WebhookEvent {
	return WebhookEvent{
		Type:      eventType,
		Source:    source,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
	}
}

// PostToWebhook sends an event to a single webhook URL and records the
// attempt in the outbox (model.WebhookDelivery) so the operator's "webhook
// log" view can answer "did my client get this notification, and if not,
// why". A receiver that answers <400 is "ok"; anything else (a 4xx/5xx,
// or a transport error) is recorded with the receiver's status or the
// error string. The outbox is a bounded append-only log: one row per
// attempt, pruned to the most recent N per URL by the retention job. A
// failed attempt does not fail the caller: the operator's client
// notification is best-effort, and a down receiver must not block the
// event it was told about.
func (s *WebhookService) PostToWebhook(url string, event WebhookEvent) error {
	if url == "" {
		return fmt.Errorf("webhook url is empty")
	}

	body, err := json.Marshal(event)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		s.recordDelivery(url, event, 0, err.Error())
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		s.recordDelivery(url, event, 0, err.Error())
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		s.recordDelivery(url, event, resp.StatusCode,
			fmt.Sprintf("webhook returned status %d", resp.StatusCode))
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	s.recordDelivery(url, event, resp.StatusCode, "ok")
	return nil
}

// recordDelivery stores one POST attempt in the outbox. A nil DB (a
// unit-test harness that never opened one) is a no-op: the log is a
// dashboard nicety, and a failing DB write here would not help the caller
// anyway (the webhook POST itself already has a definite answer).
func (s *WebhookService) recordDelivery(url string, event WebhookEvent, status int, errString string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	_ = db.Create(&model.WebhookDelivery{
		Url:          url,
		EventType:    event.Type,
		Source:       event.Source,
		Status:       errString,
		ResponseCode: status,
		At:           time.Now().UnixMilli(),
	}).Error
	// Keep the outbox bounded: drop this URL's oldest attempts beyond the
	// retention cap so a chatty event type (a high-traffic panel firing
	// user.traffic.limit_reached every 60s) cannot fill the table.
	pruneWebhookDeliveryRows(url)
}

// PostToMultipleWebhooks sends an event to several webhook URLs concurrently.
func (s *WebhookService) PostToMultipleWebhooks(urls []string, event WebhookEvent) []error {
	results := make([]error, len(urls))
	for i, url := range urls {
		results[i] = s.PostToWebhook(url, event)
	}
	return results
}

// GetConfiguredWebhookUrls reads the panel's configured webhook endpoints from
// the settings table. Returns an empty list when none are set.
func (s *WebhookService) GetConfiguredWebhookUrls() ([]string, error) {
	var settingService SettingService
	url, err := settingService.GetWebhooks()
	if err != nil {
		return nil, err
	}
	if url == "" {
		return nil, nil
	}
	// Support comma-separated URLs
	parts := strings.Split(url, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// Common event types
const (
	EventUserCreated      = "user.created"
	EventUserDeleted      = "user.deleted"
	EventUserTrafficLimit = "user.traffic.limit_reached"
	EventUserExpiryAlert  = "user.expiry.alert"
	EventNodeOnline       = "node.online"
	EventNodeOffline      = "node.offline"
	EventNodeDiskHigh     = "node.disk_high"
	EventNodeMemoryHigh   = "node.memory_high"
	EventInboundCreated   = "inbound.created"
	EventInboundDeleted   = "inbound.deleted"
)

// FireEvent is a convenience helper: fetches the configured URLs, builds the
// event, and posts it to all endpoints in the background. No-op when none are
// configured. Safe to call from any goroutine; never blocks the caller on
// network I/O.
func (s *WebhookService) FireEvent(eventType, source string, data map[string]any) {
	urls, err := s.GetConfiguredWebhookUrls()
	if err != nil || len(urls) == 0 {
		return
	}
	event := NewWebhookEvent(eventType, source, data)
	go func() {
		if errs := s.PostToMultipleWebhooks(urls, event); len(errs) > 0 {
			for _, e := range errs {
				if e != nil {
					logger.Warning("webhook delivery failed:", e)
				}
			}
		}
	}()
}

// pruneWebhookDeliveryRows drops one URL's oldest delivery attempts beyond
// the retention cap, so a chatty event type (a high-traffic panel firing
// user.traffic.limit_reached every 60s) cannot fill the table. One cheap
// DELETE per POST is the cheapest place to keep the outbox bounded without
// a dedicated job: the POST already touches the URL's rows, and the prune
// only drops rows beyond the cap, so a URL with fewer than the cap keeps
// every one of its attempts.
func pruneWebhookDeliveryRows(url string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	var ids []int
	db.Model(&model.WebhookDelivery{}).
		Where("url = ?", url).
		Order("at desc, id desc").
		Limit(model.WebhookDeliveryRetention).
		Pluck("id", &ids)
	if len(ids) == 0 {
		return
	}
	_ = db.Where("url = ? AND id NOT IN ?", url, ids).
		Delete(&model.WebhookDelivery{}).Error
}

// ListWebhookDeliveries returns one URL's delivery attempts, newest first,
// for the operator's "webhook log" view. An empty list (a URL that has never
// been POSTed to since the outbox shipped) is a valid answer, not an error.
// limit <= 0 asks for the retention cap.
func ListWebhookDeliveries(url string, limit int) ([]model.WebhookDelivery, error) {
	if limit <= 0 {
		limit = model.WebhookDeliveryRetention
	}
	var out []model.WebhookDelivery
	err := database.GetDB().
		Where("url = ?", url).
		Order("at desc, id desc").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// PruneWebhookDeliveries drops delivery attempts older than the retention
// cap, across every URL. The 60s cron already walks the panel's own gauges
// (PruneNodeMetrics), and one cheap DELETE on the same tick keeps the outbox
// bounded without a dedicated job. Rows of a URL that still has recent
// attempts are untouched; only the stale tail goes.
func PruneWebhookDeliveries() int64 {
	db := database.GetDB()
	if db == nil {
		return 0
	}
	// The per-URL cap is the retention window: keep the most recent
	// WebhookDeliveryRetention rows per URL, drop the rest. A single
	// statement per URL would be N round trips, so the whole prune is one
	// subquery that finds every row that is NOT among its URL's newest
	// cap-many, and deletes them in one shot.
	var ids []int
	db.Model(&model.WebhookDelivery{}).
		Where("id NOT IN (SELECT id FROM (SELECT id FROM webhook_deliveries ORDER BY at DESC, id DESC LIMIT ?))",
			model.WebhookDeliveryRetention*256).
		Pluck("id", &ids)
	if len(ids) == 0 {
		return 0
	}
	res := db.Where("id IN ?", ids).Delete(&model.WebhookDelivery{})
	return res.RowsAffected
}
