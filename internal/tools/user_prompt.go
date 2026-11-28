package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/chuanmingliu/golive/internal/jinja"
)

const UserPromptName = "user-prompt"

const DefaultUserPrompt = `{"enabled":false,"template":""}`

type UserPrompt struct {
	Version  int    `json:"-"`
	Enabled  bool   `json:"enabled"`
	Template string `json:"template"`
	compiled *jinja.Template
}

type UserPromptInput struct {
	CallerText        string
	WechatAdded       bool
	ProductPageOpened bool
}

func ParseUserPrompt(raw []byte) (UserPrompt, error) {
	var c *UserPrompt
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return UserPrompt{}, err
	}
	if c == nil || dec.Decode(new(any)) != io.EOF {
		return UserPrompt{}, fmt.Errorf("user prompt must be a JSON object")
	}
	if len(c.Template) > 8192 {
		return UserPrompt{}, fmt.Errorf("user prompt exceeds 8192 bytes")
	}
	if strings.TrimSpace(c.Template) == "" {
		if c.Enabled {
			return UserPrompt{}, fmt.Errorf("enter a Jinja template to enable the user prompt")
		}
		return *c, nil
	}
	tpl, err := jinja.ParseConditional(c.Template)
	if err != nil {
		return UserPrompt{}, err
	}
	for _, name := range tpl.Names() {
		if name != "caller_text" && name != "events" {
			return UserPrompt{}, fmt.Errorf("unknown user prompt variable %q", name)
		}
	}
	c.compiled = tpl
	validation := *c
	validation.Enabled = true
	for _, opened := range []bool{false, true} {
		for _, added := range []bool{false, true} {
			if _, err := validation.Render(UserPromptInput{CallerText: "示例问题", WechatAdded: added, ProductPageOpened: opened}); err != nil {
				return UserPrompt{}, err
			}
		}
	}
	return *c, nil
}

func FetchUserPrompt(ctx context.Context, base, token string) (UserPrompt, error) {
	body, version, err := fetchVoiceConfig(ctx, base, token, UserPromptName)
	if err != nil {
		return UserPrompt{}, err
	}
	c, err := ParseUserPrompt(body)
	c.Version = version
	return c, err
}

func (c UserPrompt) Render(input UserPromptInput) (string, error) {
	if !c.Enabled || c.compiled == nil {
		return "", nil
	}
	text := []rune(input.CallerText)
	rendered, err := c.compiled.Render(map[string]any{
		"caller_text": string(text[:min(len(text), 1024)]),
		"events": map[string]any{
			"wechat_added":        input.WechatAdded,
			"product_page_opened": input.ProductPageOpened,
		},
	})
	if err != nil {
		return "", err
	}
	if len(rendered) > 8192 {
		return "", fmt.Errorf("rendered user prompt exceeds 8192 bytes")
	}
	return strings.TrimSpace(rendered), nil
}
