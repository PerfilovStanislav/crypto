package telegram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trader/config"
)

func TestNotify(t *testing.T) {
	var gotPath, gotChat, gotText string
	reply := `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotChat, gotText = r.FormValue("chat_id"), r.FormValue("text")
		w.Write([]byte(reply))
	}))
	defer srv.Close()

	c := New(config.TelegramConfig{Token: "123:secret", ChatID: "42"})
	c.baseURL = srv.URL

	if err := c.Notify("SOLUSDT: opened"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot123:secret/sendMessage" || gotChat != "42" || gotText != "SOLUSDT: opened" {
		t.Errorf("path %q chat %q text %q", gotPath, gotChat, gotText)
	}

	reply = `{"ok":false,"description":"Bad Request: chat not found"}`
	if err := c.Notify("x"); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("err = %v", err)
	}
}

func TestNotifyErrorHidesToken(t *testing.T) {
	c := New(config.TelegramConfig{Token: "123:secret", ChatID: "42"})
	c.baseURL = "http://127.0.0.1:1" // nothing listens here

	err := c.Notify("x")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v", err)
	}
}

func TestNewWithoutTokenIsNil(t *testing.T) {
	if New(config.TelegramConfig{}) != nil {
		t.Error("want nil client without a token")
	}
}
