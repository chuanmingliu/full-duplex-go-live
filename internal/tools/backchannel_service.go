package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const BackchannelServiceName = "backchannel-service"
const DefaultBackchannelService = `{
  "enabled": true,
  "prompt": "你在倾听电话里的对方，对方还没有说完。结合最近对话和当前听到的片段，只输出一句极短、自然的倾听回应，例如嗯、我在听。不要回答问题、承诺或打断，不需要回应时只输出 SILENCE。"
}`

type BackchannelService struct {
	Enabled bool   `json:"enabled"`
	Prompt  string `json:"prompt"`
	Version int    `json:"-"`
}

func ParseBackchannelService(raw []byte) (BackchannelService, error) {
	var c BackchannelService
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	c.Prompt = strings.TrimSpace(c.Prompt)
	if c.Prompt == "" || len([]rune(c.Prompt)) > 8000 {
		return c, fmt.Errorf("backchannel prompt must contain 1–8000 characters")
	}
	return c, nil
}

func FetchBackchannelService(base, token string, timeout time.Duration) (BackchannelService, error) {
	raw, version, err := fetchPanelConfig(base, token, BackchannelServiceName, timeout)
	if err != nil {
		return BackchannelService{}, err
	}
	c, err := ParseBackchannelService(raw)
	c.Version = version
	return c, err
}
