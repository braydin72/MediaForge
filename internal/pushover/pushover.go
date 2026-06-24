package pushover

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const apiURL = "https://api.pushover.net/1/messages.json"

// Client sends notifications via Pushover
type Client struct {
	UserKey  string
	AppToken string
}

// NewClient creates a new Pushover client
func NewClient(userKey, appToken string) *Client {
	return &Client{
		UserKey:  userKey,
		AppToken: appToken,
	}
}

// IsConfigured returns true if both credentials are set
func (c *Client) IsConfigured() bool {
	return c.UserKey != "" && c.AppToken != ""
}

// Send sends a notification with the given title and message
func (c *Client) Send(title, message string) error {
	if !c.IsConfigured() {
		return fmt.Errorf("pushover credentials not configured")
	}

	data := url.Values{}
	data.Set("token", c.AppToken)
	data.Set("user", c.UserKey)
	data.Set("title", title)
	data.Set("message", message)

	resp, err := http.PostForm(apiURL, data)
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var result struct {
			Errors []string `json:"errors"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && len(result.Errors) > 0 {
			return fmt.Errorf("pushover error: %s", strings.Join(result.Errors, ", "))
		}
		return fmt.Errorf("pushover returned status %d", resp.StatusCode)
	}

	return nil
}

// Test sends a test notification to verify credentials
func (c *Client) Test() error {
	return c.Send("Shrinkray", "Test notification - Pushover is configured correctly!")
}
