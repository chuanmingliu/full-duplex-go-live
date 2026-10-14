package crm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/jinja"
)

// Prompts keeps templates and what has been rendered from them.
//
// A template is a prompt written in Jinja2. Every save is a new version in a
// file of its own, templates/<name>/v0003.j2, which nothing ever rewrites; a
// rendered prompt a call ran on is kept as renders/<sha>.txt. So for any call
// there are two files that say exactly what the assistant was told: the
// template version it came from and the text it became.
type Prompts struct {
	dir string
	log *Log

	mu        sync.Mutex
	templates map[string][]*Template // by name, versions in order, without their bodies
	parsed    map[string]*jinja.Template
}

var templateName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ErrNotFound is a template, a version or a render that is not there.
var ErrNotFound = errors.New("crm: not found")

func shortSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:6])
}

func (p *Prompts) versionPath(name string, version int) string {
	return filepath.Join(p.dir, "templates", name, fmt.Sprintf("v%04d.j2", version))
}

// apply takes a template.put from the log into the index.
func (p *Prompts) apply(ev Event) {
	if ev.Type != "template.put" || ev.Template == nil {
		return
	}
	t := *ev.Template
	t.Body = ""
	p.templates[t.Name] = append(p.templates[t.Name], &t)
}

// Save stores text as the next version of the template called name.
func (p *Prompts) Save(name, body, note, by string) (*Template, error) {
	if !templateName.MatchString(name) {
		return nil, fmt.Errorf("crm: a template's name is lower-case letters, digits, - and _, up to 64 of them; %q is not", name)
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("crm: the template is empty")
	}
	parsed, err := jinja.Parse(body)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	versions := p.templates[name]
	sha := shortSHA(body)
	if n := len(versions); n > 0 && versions[n-1].SHA == sha {
		t := *versions[n-1]
		return &t, nil // the same text again is the same version
	}
	t := &Template{Name: name, Version: len(versions) + 1, SHA: sha, Chars: len([]rune(body)), Note: note,
		Names: parsed.Names(), StaticChars: parsed.FirstVariable(), By: by}
	path := p.versionPath(name, t.Version)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("crm: %w", err)
	}
	if err := writeFileAtomic(path, []byte(body)); err != nil {
		return nil, err
	}
	t.CreatedAt = time.Now().Round(time.Millisecond)
	if _, err := p.log.Append(Event{At: t.CreatedAt, By: by, Type: "template.put", Ref: fmt.Sprintf("%s v%d", name, t.Version), Template: t, Note: note}); err != nil {
		os.Remove(path)
		return nil, err
	}
	p.templates[name] = append(versions, t)
	p.parsed[fmt.Sprintf("%s/%d", name, t.Version)] = parsed
	out := *t
	return &out, nil
}

// List is every template's latest version, by name.
func (p *Prompts) List() []*Template {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Template, 0, len(p.templates))
	for _, versions := range p.templates {
		t := *versions[len(versions)-1]
		out = append(out, &t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Versions is every version of one template, oldest first.
func (p *Prompts) Versions(name string) []*Template {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Template, 0, len(p.templates[name]))
	for _, v := range p.templates[name] {
		t := *v
		out = append(out, &t)
	}
	return out
}

// Get returns one version of a template with its text; version 0 is the
// latest.
func (p *Prompts) Get(name string, version int) (*Template, error) {
	p.mu.Lock()
	versions := p.templates[name]
	if version == 0 {
		version = len(versions)
	}
	if version < 1 || version > len(versions) {
		p.mu.Unlock()
		return nil, ErrNotFound
	}
	t := *versions[version-1]
	p.mu.Unlock()
	body, err := os.ReadFile(p.versionPath(name, version))
	if err != nil {
		return nil, fmt.Errorf("crm: template %s v%d is in the log and its file is gone: %w", name, version, err)
	}
	if sha := shortSHA(string(body)); sha != t.SHA {
		return nil, fmt.Errorf("crm: template %s v%d has been changed on disk since it was saved (it reads %s, it was saved as %s)", name, version, sha, t.SHA)
	}
	t.Body = string(body)
	return &t, nil
}

// Rendered is a prompt made from a template for one set of variables.
type Rendered struct {
	Template string `json:"template"`
	Version  int    `json:"version"`
	// TemplateSHA names the template's text, and SHA the rendered prompt's.
	TemplateSHA  string `json:"template_sha"`
	SHA          string `json:"sha"`
	Instructions string `json:"instructions"`
	Chars        int    `json:"chars"`
	// Supplied are the variables given; Undefined those the template reads
	// that were not given, which render as Jinja2 renders a missing value —
	// as nothing, or as whichever branch the template takes for it; Unused
	// those given that the template never reads, which is how a misspelt
	// name shows.
	Supplied  []string `json:"supplied"`
	Undefined []string `json:"undefined"`
	Unused    []string `json:"unused"`
	// Defaulted are the variables filled in from the service's defaults (the
	// default opening line as start).
	Defaulted []string `json:"defaulted,omitempty"`
	// StaticChars is how much of the template comes before its first
	// variable: the part a model's prompt cache can keep between leads.
	StaticChars int `json:"static_chars"`
}

// Render fills a template in. Version 0 is the latest. A variable called
// nowDateTime that is not supplied is given the time of the render, as
// "2006-01-02 15:04:05" in loc.
func (p *Prompts) Render(name string, version int, vars map[string]any, now time.Time) (*Rendered, error) {
	return p.RenderWith(name, version, vars, now, nil)
}

// RenderWith is Render with defaults: variables the template reads that the
// request does not give are taken from defaults first, and are reported as
// supplied by default rather than undefined.
func (p *Prompts) RenderWith(name string, version int, vars map[string]any, now time.Time, defaults map[string]any) (*Rendered, error) {
	t, err := p.Get(name, version)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	key := fmt.Sprintf("%s/%d", t.Name, t.Version)
	parsed := p.parsed[key]
	p.mu.Unlock()
	if parsed == nil {
		if parsed, err = jinja.Parse(t.Body); err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.parsed[key] = parsed
		p.mu.Unlock()
	}
	given := map[string]any{}
	for k, v := range vars {
		given[k] = v
	}
	reads := map[string]bool{}
	for _, n := range t.Names {
		reads[n] = true
	}
	if _, has := given["nowDateTime"]; !has && reads["nowDateTime"] {
		given["nowDateTime"] = now.Format("2006-01-02 15:04:05")
	}
	var defaulted []string
	for k, v := range defaults {
		if _, has := given[k]; !has && reads[k] && v != nil && v != "" {
			given[k] = v
			defaulted = append(defaulted, k)
		}
	}
	sort.Strings(defaulted)
	text, err := parsed.Render(given)
	if err != nil {
		return nil, err
	}
	r := &Rendered{Template: t.Name, Version: t.Version, TemplateSHA: t.SHA, SHA: shortSHA(text), Instructions: text,
		Chars: len([]rune(text)), StaticChars: t.StaticChars, Supplied: []string{}, Undefined: []string{}, Unused: []string{}, Defaulted: defaulted}
	for k := range vars {
		r.Supplied = append(r.Supplied, k)
		if !reads[k] {
			r.Unused = append(r.Unused, k)
		}
	}
	for _, n := range t.Names {
		if _, has := given[n]; !has {
			r.Undefined = append(r.Undefined, n)
		}
	}
	sort.Strings(r.Supplied)
	sort.Strings(r.Unused)
	return r, nil
}

// Keep writes a rendered prompt down, so that a call placed with it can
// always be answered for.
func (p *Prompts) Keep(r *Rendered) error {
	dir := filepath.Join(p.dir, "renders")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("crm: %w", err)
	}
	path := filepath.Join(dir, r.SHA+".txt")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return writeFileAtomic(path, []byte(r.Instructions))
}

var renderSHA = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Kept returns a rendered prompt that was written down.
func (p *Prompts) Kept(sha string) (string, error) {
	if !renderSHA.MatchString(sha) {
		return "", ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "renders", sha+".txt"))
	if err != nil {
		return "", ErrNotFound
	}
	return string(data), nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("crm: %w", err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("crm: %w", err)
	}
	return nil
}

// decodeVars reads a JSON object of variables, keeping whole numbers whole.
func decodeVars(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var vars map[string]any
	if err := dec.Decode(&vars); err != nil {
		return nil, errors.New("crm: vars must be a JSON object of names and values")
	}
	if vars == nil {
		vars = map[string]any{}
	}
	return vars, nil
}
