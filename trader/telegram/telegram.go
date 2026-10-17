// Package telegram sends messages through the Telegram Bot API.
package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"trader/config"
)

const defaultBaseURL = "https://api.telegram.org"

type Client struct {
	token, chatID string
	baseURL       string
	httpClient    *http.Client
}

// New returns nil when the token is not set: the bot then sends nothing.
func New(cfg config.TelegramConfig) *Client {
	if cfg.Token == "" {
		return nil
	}
	return &Client{
		token:      cfg.Token,
		chatID:     cfg.ChatID,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Notify sends plain text to the configured chat. A nil client sends nothing.
func (c *Client) Notify(text string) error {
	if c == nil {
		return nil
	}
	resp, err := c.httpClient.PostForm(c.baseURL+"/bot"+c.token+"/sendMessage", url.Values{
		"chat_id": {c.chatID},
		"text":    {text},
	})
	if err != nil {
		// The request URL contains the bot token: never let it reach the logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("telegram: HTTP %d: %w", resp.StatusCode, err)
	}
	if !body.OK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, body.Description)
	}
	return nil
}
