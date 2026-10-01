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
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	cfg := Config{
		Channel:            ChannelWebhook,
		WebhookURL:         srv.URL,
		WebhookHeaderName:  "Authorization",
		WebhookHeaderValue: "Bearer sekret",
	}
	msg := Message{Event: "complete", Title: "done", Body: "all good", Task: TaskInfo{Script: "deploy.sh"}}
	if err := Send(context.Background(), srv.Client(), cfg, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Event != "complete" || got.Title != "done" || got.Task.Script != "deploy.sh" {
		t.Fatalf("webhook payload = %+v", got)
	}
	if authHeader != "Bearer sekret" {
		t.Fatalf("custom header not sent: %q", authHeader)
	}
}

func TestSendWebhookWithoutHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
	}))
	defer srv.Close()

	cfg := Config{Channel: ChannelWebhook, WebhookURL: srv.URL}
	if err := Send(context.Background(), srv.Client(), cfg, Message{Title: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if hadAuth {
		t.Fatal("no header configured, yet one was sent")
	}
}

func TestParseHeader(t *testing.T) {
	cases := []struct {
		in        string
		name, val string
	}{
		{"Authorization: Bearer x", "Authorization", "Bearer x"},
		{"  X-Token :  abc  ", "X-Token", "abc"},
		{"no-colon", "", ""},
		{"", "", ""},
		{"X-Empty:", "X-Empty", ""},
	}
	for _, c := range cases {
		if n, v := ParseHeader(c.in); n != c.name || v != c.val {
			t.Errorf("ParseHeader(%q) = (%q, %q), want (%q, %q)", c.in, n, v, c.name, c.val)
		}
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
