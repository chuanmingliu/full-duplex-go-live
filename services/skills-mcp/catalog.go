package main

import (
	"context"
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
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed quotation/SKILL.md
var seed string

//go:embed wechat/SKILL.md
var wechatSeed string

const maxCatalogSkills = 100000
const maxCatalogBytes = 64 << 20

type Skill struct {
	Priority    int                 `json:"priority,omitempty"`
	Requires    []string            `json:"requires,omitempty"`
	Conflicts   []string            `json:"conflicts,omitempty"`
	Match       []string            `json:"match,omitempty"`
	Exclude     []string            `json:"exclude,omitempty"`
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
	SkillSelection  *SkillSelectionConfig    `json:"skill_selection,omitempty"`
	Schema          int                      `json:"schema"`
	Revision        int                      `json:"revision"`
	Skills          []Skill                  `json:"skills"`
	Quote           ToolConfig               `json:"quote_total"`
	EventResponses  map[string]EventResponse `json:"event_responses,omitempty"`
	PromptRendering *PromptRendering         `json:"prompt_rendering,omitempty"`
}

type PromptRendering struct {
	Version int `json:"version"`
}

func (c Catalog) renderingVersion() int {
	if c.PromptRendering == nil {
		return 1
	}
	return c.PromptRendering.Version
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
	quote.Match = []string{`(报个价|给.*报价|帮.*报价|重新.*(算|报价)|再(算|报价)|算一下|算.*(保费|价格)|修改.*(报价|方案)|对比.*(报价|方案)|比较.*(报价|方案)|^报价[吧。？?！!]*$)`}
	quote.Match = append(quote.Match, `(三者|车损|划痕|司机|乘客|精神损害|医保外|免赔率|假日|车船税).*(改成|改为|加上|取消|不投保|多少钱|比较|对比)`, `(加上|取消|改成|改为|不投保).*(三者|车损|划痕|司机|乘客|精神损害|医保外|免赔率|假日|车船税)`)
	quote.Match = append(quote.Match, `(三者|车损|划痕|司机|乘客|医保外|医保费).*(比.+(多|少|贵|便宜)多少|加个|加到|改到|各.*(钱|价格|保费)|每.*(钱|价格|保费))`)
	quote.Match = append(quote.Match, `(续保|保费|保险|报价).*(多少钱|多少|价格)`, `(价格多少钱|续保大概多少钱)`, `(三者|车损|司机|乘客|医保外).*(按照|按).*(算|报)`)
	quote.Match = append(quote.Match, `(比较|对比).*(三者|车损|司机|乘客|医保外|划痕|精神损害|免赔率)`)
	quote.Exclude = []string{`(不要|不用|别|不需要).*(报价|算|比较|对比)`, `(什么是|是什么|为什么).*(报价|保费)`}
	wechat.Match = []string{`(微信|服务通知).*(哪里|在哪|找|打开|怎么|如何|下一步|接下来|添加|加你)`, `(哪里|怎么|如何|找不到).*(微信|服务通知)`, `^(下一步|接下来)(呢|该.*|怎么.*)?[？?。]?$`}
	wechat.Match = append(wechat.Match, `(服务通知|公众号).*(还是|普通|通知|公众号)`, `(发短信|加[Vv微]).*(加|是吧|怎么|哪里)`, `(我看一下|看到了|加上了|发过来了).*(是吧|看到|看见|吧|了)?`)
	wechat.Exclude = []string{`(不想|不要|不用|不需要|拒绝|别).*(微信|添加|加你)`, `(什么是|是什么|为什么)`}
	return Catalog{Schema: 3, Revision: 1, Skills: []Skill{quote, wechat}, Quote: ToolConfig{Description: "Calculate the default motor insurance quote, a revised selection, or multiple alternatives using the existing CRM rules and call memory.", TimeoutSeconds: 90, Enabled: true}}
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

var skillName = regexp.MustCompile("^[a-z0-9]+([-_][a-z0-9]+)*$")

func (c Catalog) validate() error {
	if err := c.validateSelectionSettings(); err != nil {
		return err
	}
	if version := c.renderingVersion(); version != 1 && version != 2 {
		return errors.New("prompt rendering version must be 1 or 2")
	}
	if err := validateEventResponses(c.EventResponses); err != nil {
		return err
	}
	if c.Schema != 3 {
		return errors.New("unsupported catalog schema")
	}
	if len(c.Skills) > maxCatalogSkills {
		return errors.New("maximum 100000 skills")
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
			return errors.New("skill names must be unique lowercase words up to 64 characters, optionally joined by hyphens or underscores")
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
	if err := validateSkillRelations(c); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(data) > maxCatalogBytes {
		return errors.New("catalog exceeds 64 MiB")
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
			skillSelectionExtension: map[string]any{"priority": s.Priority, "requires": s.Requires, "conflicts": s.Conflicts},
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
	mu         sync.RWMutex
	path       string
	catalog    Catalog
	server     *mcp.Server
	codeServer *mcp.Server
	backend    backend
	index      *skillIndex
	embedder   *embeddingClient
}

func openStore(path string, b backend) (*store, error) {
	return openStoreWithEmbeddings(path, b, nil)
}

func openStoreWithEmbeddings(path string, b backend, embedder *embeddingClient) (*store, error) {
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
	if c.Schema == 2 {
		c.migrateRouting()
		needsSave = true
	}
	if err = c.validate(); err != nil {
		return nil, err
	}
	if c.Revision < 1 {
		return nil, errors.New("invalid catalog revision")
	}
	index := newSkillIndex(c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := index.prepareEmbeddings(ctx, embedder, nil); err != nil {
		return nil, err
	}
	server, codeServer := serversWithIndex(c, b, index)
	s := &store{path: path, backend: b, catalog: c, server: server, codeServer: codeServer, index: index, embedder: embedder}
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
	return s.publishContext(context.Background(), c)
}

func (s *store) publishContext(ctx context.Context, c Catalog) (Catalog, error) {
	if err := c.validate(); err != nil {
		return Catalog{}, err
	}
	s.mu.RLock()
	previous := s.index
	currentRevision := s.catalog.Revision
	s.mu.RUnlock()
	if c.Revision != currentRevision {
		return Catalog{}, errConflict
	}
	c.Revision++
	index := newSkillIndex(c)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := index.prepareEmbeddings(ctx, s.embedder, previous); err != nil {
		return Catalog{}, err
	}
	server, codeServer := serversWithIndex(c, s.backend, index)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalog.Revision != currentRevision {
		return Catalog{}, errConflict
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	if err := s.persist(c); err != nil {
		return Catalog{}, err
	}
	s.catalog = c
	s.index = index
	s.server = server
	s.codeServer = codeServer
	return c, nil
}

// Add editable fast-path rules only to the known seeded workflows. Preserve
// custom skills and every operator-authored instruction and state guard.
func (c *Catalog) migrateRouting() {
	for i := range c.Skills {
		for _, seed := range defaults().Skills {
			if c.Skills[i].Name == seed.Name && c.Skills[i].Match == nil {
				c.Skills[i].Match = seed.Match
				c.Skills[i].Exclude = seed.Exclude
			}
		}
	}
	c.Schema = 3
	c.Revision++
}
