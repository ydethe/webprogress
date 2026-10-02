// Package notify delivers out-of-band alerts about tracked tasks to a user's
// chosen channel — Pushover, a Slack incoming webhook, or a custom HTTP webhook.
//
// It is orthogonal to the live dashboard: the Dispatcher watches the same bus
// of progress updates the WebSocket feed consumes and fires a one-shot alert
// when a task completes or goes silent (stalls). A Config describes a single
// user's channel and credentials; it is produced either from server-wide env
// defaults (config.Settings.DefaultNotify) or, taking precedence, from the
// user's own persisted settings (storage). This package never imports config or
// storage, so both may depend on it without a cycle.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Channel is the delivery mechanism a Config uses. The empty channel means
// notifications are disabled.
type Channel string

const (
	ChannelNone     Channel = ""
	ChannelPushover Channel = "pushover"
	ChannelSlack    Channel = "slack"
	ChannelWebhook  Channel = "webhook"
)

// pushoverAPIURL is the Pushover message endpoint. It is a package var (not a
// const) so tests can point it at an httptest server; Slack and the custom
// webhook already carry their URL in the Config.
var pushoverAPIURL = "https://api.pushover.net/1/messages.json"

// Config is one user's notification setup. The zero value (ChannelNone) is
// valid and disabled. Only the fields relevant to Channel are consulted.
type Config struct {
	Channel         Channel
	PushoverToken   string // Pushover application API token
	PushoverUser    string // Pushover user/group key
	SlackWebhookURL string // Slack incoming-webhook URL
	WebhookURL      string // custom webhook URL (receives the JSON in WebhookPayload)
	// WebhookHeaderName/Value is an optional extra HTTP header sent with the
	// custom webhook request (e.g. "Authorization: Bearer …"). Empty name = none.
	WebhookHeaderName  string
	WebhookHeaderValue string
	// StallSeconds is how long a task may go without an update before it is
	// reported as stalled. Zero disables stall notifications; completion
	// notifications fire regardless.
	StallSeconds int
}

// Enabled reports whether this config would deliver a notification.
func (c Config) Enabled() bool { return c.Channel != ChannelNone }

// Validate checks that the fields required by the selected channel are present.
func (c Config) Validate() error {
	switch c.Channel {
	case ChannelNone:
		// disabled; nothing else matters
	case ChannelPushover:
		if c.PushoverToken == "" || c.PushoverUser == "" {
			return fmt.Errorf("pushover requires an application token and a user key")
		}
	case ChannelSlack:
		if c.SlackWebhookURL == "" {
			return fmt.Errorf("slack requires an incoming-webhook URL")
		}
	case ChannelWebhook:
		if c.WebhookURL == "" {
			return fmt.Errorf("webhook requires a URL")
		}
	default:
		return fmt.Errorf("unknown channel %q", c.Channel)
	}
	if c.StallSeconds < 0 {
		return fmt.Errorf("stall timeout must not be negative")
	}
	return nil
}

// TaskInfo is the task a notification is about, carried verbatim in the custom
// webhook payload so a receiver can route on it.
type TaskInfo struct {
	Script      string  `json:"script"`
	Host        string  `json:"host"`
	Login       string  `json:"login"`
	Description string  `json:"description"`
	Progress    float64 `json:"progress"`
	Total       float64 `json:"total"`
	Fraction    float64 `json:"fraction"`
	// Library is the reporting client library as "name@version" (e.g.
	// "webprogress@1.2.3"), empty when the reporter sent none. Criticity is the
	// task's importance level ("trivial"/"standard"/"critical") that gated this
	// notification. Both let a webhook receiver route on richer task metadata.
	Library   string `json:"library"`
	Criticity string `json:"criticity"`
}

// Message is a single rendered notification, independent of the channel that
// will carry it.
type Message struct {
	Event string // "complete", "stalled", "dead", or "test"
	Title string
	Body  string
	Task  TaskInfo
}

// WebhookPayload is the JSON body POSTed to a custom webhook.
type WebhookPayload struct {
	Event   string   `json:"event"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Task    TaskInfo `json:"task"`
}

// Send delivers msg over cfg's channel. A disabled config is a no-op. The
// context bounds the outbound HTTP request.
func Send(ctx context.Context, client *http.Client, cfg Config, msg Message) error {
	if client == nil {
		client = http.DefaultClient
	}
	switch cfg.Channel {
	case ChannelNone:
		return nil
	case ChannelPushover:
		return sendPushover(ctx, client, cfg, msg)
	case ChannelSlack:
		return sendSlack(ctx, client, cfg, msg)
	case ChannelWebhook:
		return sendWebhook(ctx, client, cfg, msg)
	default:
		return fmt.Errorf("unknown channel %q", cfg.Channel)
	}
}

func sendPushover(ctx context.Context, client *http.Client, cfg Config, msg Message) error {
	form := url.Values{
		"token":   {cfg.PushoverToken},
		"user":    {cfg.PushoverUser},
		"title":   {msg.Title},
		"message": {msg.Body},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverAPIURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(client, req, "pushover")
}

func sendSlack(ctx context.Context, client *http.Client, cfg Config, msg Message) error {
	text := msg.Title
	if msg.Body != "" {
		text += "\n" + msg.Body
	}
	return postJSON(ctx, client, cfg.SlackWebhookURL, map[string]string{"text": text}, nil, "slack")
}

func sendWebhook(ctx context.Context, client *http.Client, cfg Config, msg Message) error {
	var headers map[string]string
	if cfg.WebhookHeaderName != "" {
		headers = map[string]string{cfg.WebhookHeaderName: cfg.WebhookHeaderValue}
	}
	return postJSON(ctx, client, cfg.WebhookURL, WebhookPayload{
		Event:   msg.Event,
		Title:   msg.Title,
		Message: msg.Body,
		Task:    msg.Task,
	}, headers, "webhook")
}

func postJSON(ctx context.Context, client *http.Client, rawURL string, body any, headers map[string]string, label string) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(client, req, label)
}

func do(client *http.Client, req *http.Request, label string) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: unexpected status %d", label, resp.StatusCode)
	}
	return nil
}

// ParseHeader splits a "Name: Value" header string into its name and value,
// trimming surrounding spaces. An empty or colon-less string yields two empty
// strings. Used to accept the webhook header from a single env var.
func ParseHeader(s string) (name, value string) {
	name, value, found := strings.Cut(s, ":")
	if !found {
		return "", ""
	}
	return strings.TrimSpace(name), strings.TrimSpace(value)
}
