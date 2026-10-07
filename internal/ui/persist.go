package ui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/lengh/upper/internal/config"
)

// uiState is what upper remembers between runs, so it reopens where you
// left off: the last channel (for Alt+/) and unsent drafts.
type uiState struct {
	LastChannel Snowflake            `json:"last_channel,omitempty"`
	PrevChannel Snowflake            `json:"prev_channel,omitempty"`
	Sidebar     *bool                `json:"sidebar,omitempty"`
	Drafts      map[Snowflake]string `json:"drafts,omitempty"`
}

func uiStatePath() string {
	dir, err := config.StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ui.json")
}

func loadUIState() uiState {
	var s uiState
	if p := uiStatePath(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(data, &s)
		}
	}
	return s
}

func saveUIState(s uiState) {
	p := uiStatePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}
