// Package config loads upper's settings and stores the login token.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	// TimeFormat is a Go time layout for message timestamps; "" hides them.
	TimeFormat string `json:"time_format"`
	// HistoryPage is how many messages to fetch per page (max 100).
	HistoryPage int `json:"history_page"`
	// MaxMessages caps the in-memory messages per channel.
	MaxMessages int `json:"max_messages"`
	// MaxChannels caps how many channels keep cached history.
	MaxChannels int `json:"max_channels"`
	// SidebarWidth is the width of the server/channel tree.
	SidebarWidth int `json:"sidebar_width"`
	// BellOnMention rings the terminal bell on mentions and DMs. In Windows
	// Terminal this flashes the taskbar icon.
	BellOnMention bool `json:"bell_on_mention"`
	// SendTyping broadcasts your typing indicator.
	SendTyping bool `json:"send_typing"`
	// MarkRead acknowledges channels you view, syncing unread state with
	// your other devices.
	MarkRead bool `json:"mark_read"`
	// MentionOnReply pings the author of the message you reply to.
	MentionOnReply bool `json:"mention_on_reply"`
	// Theme colours (names or #rrggbb).
	Theme Theme `json:"theme"`
}

type Theme struct {
	Border    string `json:"border"`
	Accent    string `json:"accent"`
	Timestamp string `json:"timestamp"`
	Muted     string `json:"muted"`
	Mention   string `json:"mention"`
	Unread    string `json:"unread"`
	Selected  string `json:"selected"`
	Error     string `json:"error"`
}

func Default() Config {
	return Config{
		TimeFormat:     "15:04",
		HistoryPage:    50,
		MaxMessages:    400,
		MaxChannels:    64,
		SidebarWidth:   32,
		BellOnMention:  true,
		SendTyping:     true,
		MarkRead:       true,
		MentionOnReply: true,
		Theme: Theme{
			Border:    "#5865f2",
			Accent:    "#5865f2",
			Timestamp: "#72767d",
			Muted:     "#8e9297",
			Mention:   "#faa61a",
			Unread:    "#ffffff",
			Selected:  "#404249",
			Error:     "#ed4245",
		},
	}
}

// Dir returns the configuration directory ($XDG_CONFIG_HOME/upper).
func Dir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "upper"), nil
}

// Load reads path (or the default location when empty), falling back to
// defaults for anything missing.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		dir, err := Dir()
		if err != nil {
			return cfg, nil
		}
		path = filepath.Join(dir, "config.json")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	cfg.HistoryPage = clamp(cfg.HistoryPage, 10, 100)
	cfg.MaxMessages = clamp(cfg.MaxMessages, 100, 5000)
	cfg.MaxChannels = clamp(cfg.MaxChannels, 4, 1000)
	cfg.SidebarWidth = clamp(cfg.SidebarWidth, 16, 80)
	return cfg, nil
}

func clamp(v, lo, hi int) int {
	return max(lo, min(hi, v))
}

func tokenPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}

// LoadToken returns the token from $UPPER_TOKEN or the token file.
func LoadToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("UPPER_TOKEN")); t != "" {
		return t, nil
	}
	p, err := tokenPath()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		// Someone loosened the permissions; tighten them again.
		_ = os.Chmod(p, 0o600)
	}
	data, err := os.ReadFile(p)
	return strings.TrimSpace(string(data)), err
}

// SaveToken writes the token readable only by the current user.
func SaveToken(token string) (string, error) {
	p, err := tokenPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// DeleteToken removes the stored token.
func DeleteToken() error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
