package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed quotation/SKILL.md
var seed string

//go:embed wechat/SKILL.md
var wechatSeed string

type Skill struct {
	Tool        string              `json:"tool,omitempty"`
	Events      []string            `json:"events,omitempty"`
	State       map[string][]string `json:"state,omitempty"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Body        string              `json:"body"`
	Enabled     bool                `json:"enabled"`
}

type ToolConfig struct {
	Description    string `json:"description"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Enabled        bool   `json:"enabled"`
}

type Catalog struct {
	Schema   int        `json:"schema"`
	Revision int        `json:"revision"`
	Skills   []Skill    `json:"skills"`
	Quote    ToolConfig `json:"quote_total"`
}

func seedSkill(markdown string) Skill {
	header, body, ok := strings.Cut(strings.TrimPrefix(markdown, "---\n"), "\n---\n")
	fields := strings.Split(header, "\n")
	if !ok || len(fields) != 2 {
		panic("invalid embedded skill")
	}
	return Skill{Name: strings.TrimPrefix(fields[0], "name: "), Description: strings.TrimPrefix(fields[1], "description: "), Body: strings.TrimSpace(body) + "\n", Enabled: true}
}
func defaults() Catalog {
	quote := seedSkill(seed)
	quote.Tool = "quote_total"
	quote.Events = []string{"quote.requested", "quote.revised", "quote.compare"}
	wechat := seedSkill(wechatSeed)
	wechat.Events = []string{"wechat.add_requested", "wechat.notification_help"}
	wechat.State = map[string][]string{"wechat.status": {"consented", "awaiting_confirmation"}}
	return Catalog{Schema: 2, Revision: 1, Skills: []Skill{quote, wechat}, Quote: ToolConfig{Description: "Calculate the default motor insurance quote, a revised selection, or multiple alternatives using the existing CRM rules and call memory.", TimeoutSeconds: 90, Enabled: true}}
}

// The original catalog implicitly tied every skill to quote_total. Preserve
// operator edits and that dependency when introducing independent workflows.
func (c *Catalog) migrate() {
	d := defaults()
	addWechat := true
	for i := range c.Skills {
		sk := &c.Skills[i]
		sk.Tool = "quote_total"
		if sk.Name == d.Skills[0].Name {
			sk.Events = d.Skills[0].Events
		}
		if sk.Name == d.Skills[1].Name {
			addWechat = false
		}
	}
	if addWechat {
		c.Skills = append(c.Skills, d.Skills[1])
	}
	c.Schema = 2
	c.Revision++
}

var skillName = regexp.MustCompile("^[a-z0-9]+(-[a-z0-9]+)*$")

func (c Catalog) validate() error {
	if c.Schema != 2 {
		return errors.New("unsupported catalog schema")
	}
	if len(c.Skills) > 64 {
		return errors.New("maximum 64 skills")
	}
	if c.Quote.TimeoutSeconds < 5 || c.Quote.TimeoutSeconds > 180 {
		return errors.New("tool timeout must be 5–180 seconds")
	}
	if strings.TrimSpace(c.Quote.Description) == "" || len(c.Quote.Description) > 4096 {
		return errors.New("tool description must be 1–4096 bytes")
	}
	names := map[string]bool{}
	for _, s := range c.Skills {
		if !skillName.MatchString(s.Name) || len(s.Name) > 64 || names[s.Name] {
			return errors.New("skill names must be unique lowercase words up to 64 characters, optionally joined by hyphens")
		}
		names[s.Name] = true
		if err := s.validateBindings(); err != nil {
			return err
		}
		if strings.TrimSpace(s.Description) == "" || utf8.RuneCountInString(s.Description) > 1024 || strings.ContainsAny(s.Description, "\r\n") {
			return errors.New("provide a one-line skill description of up to 1024 characters")
		}
		if strings.TrimSpace(s.Body) == "" || len(s.Body) > 64<<10 {
			return errors.New("skill body must be 1–65536 bytes")
		}
	}
	return nil
}
func (s Skill) uri() string { return "skill://" + s.Name + "/SKILL.md" }
func (s Skill) markdown() string {
	d, _ := json.Marshal(s.Description)
	// JSON quoted strings are YAML double-quoted scalars. Quote names too (e.g. 'true').
	n, _ := json.Marshal(s.Name)
	return "---\nname: " + string(n) + "\ndescription: " + string(d) + "\n---\n\n" + s.Body
}

type fileInfo struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}
type entry struct {
	Meta        map[string]any    `json:"_meta,omitempty"`
	URI         string            `json:"uri"`
	Frontmatter map[string]string `json:"frontmatter"`
	Resources   []fileInfo        `json:"resources"`
}

func (s Skill) entry() entry {
	data := s.markdown()
	return entry{
		Meta: map[string]any{
			"ai.liuchuanming.golive/routing": map[string]any{
				"events": s.Events,
				"state":  s.State,
				"tool":   s.Tool,
			},
		},
		URI:         s.uri(),
		Frontmatter: map[string]string{"name": s.Name, "description": s.Description},
		Resources: []fileInfo{{
			URI: s.uri(), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data))), Size: len(data),
		}},
	}
}

type store struct {
	mu      sync.RWMutex
	path    string
	catalog Catalog
	server  *mcp.Server
	backend backend
}

func openStore(path string, b backend) (*store, error) {
	c := defaults()
	data, err := os.ReadFile(path)
	needsSave := os.IsNotExist(err)
	if err == nil {
		c = Catalog{}
		if err = json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if !needsSave && c.Schema == 0 {
		c.migrate()
		needsSave = true
	}
	if err = c.validate(); err != nil {
		return nil, err
	}
	if c.Revision < 1 {
		return nil, errors.New("invalid catalog revision")
	}
	s := &store{path: path, backend: b, catalog: c, server: buildServer(c, b)}
	if needsSave {
		if err = s.persist(c); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *store) persist(c Catalog) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".catalog-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.path)
}

var errConflict = errors.New("another editor published a revision; reload before saving")

func (s *store) publish(c Catalog) (Catalog, error) {
	if err := c.validate(); err != nil {
		return Catalog{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Revision != s.catalog.Revision {
		return Catalog{}, errConflict
	}
	c.Revision++
	server := buildServer(c, s.backend)
	if err := s.persist(c); err != nil {
		return Catalog{}, err
	}
	s.catalog = c
	s.server = server
	return c, nil
}
