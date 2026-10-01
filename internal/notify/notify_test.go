package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"disabled", Config{}, true},
		{"pushover ok", Config{Channel: ChannelPushover, PushoverToken: "t", PushoverUser: "u"}, true},
		{"pushover missing user", Config{Channel: ChannelPushover, PushoverToken: "t"}, false},
		{"slack ok", Config{Channel: ChannelSlack, SlackWebhookURL: "https://x"}, true},
		{"slack missing url", Config{Channel: ChannelSlack}, false},
		{"webhook ok", Config{Channel: ChannelWebhook, WebhookURL: "https://x"}, true},
		{"webhook missing url", Config{Channel: ChannelWebhook}, false},
		{"unknown channel", Config{Channel: "carrier-pigeon"}, false},
		{"negative stall", Config{StallSeconds: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.cfg.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate() err = %v, want ok = %v", err, c.ok)
			}
		})
	}
}

func TestSendSlack(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	cfg := Config{Channel: ChannelSlack, SlackWebhookURL: srv.URL}
	if err := Send(context.Background(), srv.Client(), cfg, Message{Title: "hi", Body: "there"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got["text"] != "hi\nthere" {
		t.Fatalf("slack text = %q", got["text"])
	}
}

func TestSendWebhook(t *testing.T) {
	var got WebhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	cfg := Config{Channel: ChannelWebhook, WebhookURL: srv.URL}
	msg := Message{Event: "complete", Title: "done", Body: "all good", Task: TaskInfo{Script: "deploy.sh"}}
	if err := Send(context.Background(), srv.Client(), cfg, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Event != "complete" || got.Title != "done" || got.Task.Script != "deploy.sh" {
		t.Fatalf("webhook payload = %+v", got)
	}
}

func TestSendPushover(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
	}))
	defer srv.Close()

	orig := pushoverAPIURL
	pushoverAPIURL = srv.URL
	defer func() { pushoverAPIURL = orig }()

	cfg := Config{Channel: ChannelPushover, PushoverToken: "app-tok", PushoverUser: "usr"}
	if err := Send(context.Background(), srv.Client(), cfg, Message{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if form.Get("token") != "app-tok" || form.Get("user") != "usr" || form.Get("message") != "b" {
		t.Fatalf("pushover form = %v", form)
	}
}

func TestSendDisabledIsNoop(t *testing.T) {
	if err := Send(context.Background(), http.DefaultClient, Config{}, Message{}); err != nil {
		t.Fatalf("disabled Send should be a no-op, got %v", err)
	}
}

func TestSendNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{Channel: ChannelSlack, SlackWebhookURL: srv.URL}
	if err := Send(context.Background(), srv.Client(), cfg, Message{Title: "x"}); err == nil {
		t.Fatal("expected error on 500 response")
	}
}
