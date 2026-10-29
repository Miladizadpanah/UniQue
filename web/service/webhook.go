package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
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

// PostToWebhook sends an event to a single webhook URL.
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
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
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
	EventInboundCreated   = "inbound.created"
	EventInboundDeleted   = "inbound.deleted"
)
