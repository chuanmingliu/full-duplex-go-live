package crm

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const outboundTag = "speko-outbound"

const scheduleTag = "schedule"
const scheduleTimezone = "Asia/Shanghai"

var leadPhone = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
var leadKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// CallPlan records a weekly wall-clock recurrence. Days use cron numbering (Sunday = 0).
type CallPlan struct {
	Time     string `json:"time"`
	Days     []int  `json:"days"`
	Timezone string `json:"timezone,omitempty"`
	Cron     string `json:"cron,omitempty"`
}

func (p CallPlan) normalized() (*CallPlan, error) {
	if p.Time == "" && len(p.Days) == 0 {
		return nil, nil
	}
	at, err := time.Parse("15:04", p.Time)
	if err != nil || at.Format("15:04") != p.Time {
		return nil, fail(http.StatusUnprocessableEntity, "choose a call time in HH:MM format")
	}
	p.Days = slices.Clone(p.Days)
	slices.Sort(p.Days)
	p.Days = slices.Compact(p.Days)
	if len(p.Days) == 0 || p.Days[0] < 0 || p.Days[len(p.Days)-1] > 6 {
		return nil, fail(http.StatusUnprocessableEntity, "choose at least one valid repeat day")
	}
	days := make([]string, len(p.Days))
	for i, day := range p.Days {
		days[i] = strconv.Itoa(day)
	}
	dow := strings.Join(days, ",")
	if len(days) == 7 {
		dow = "*"
	}
	p.Cron = fmt.Sprintf("%d %d * * %s", at.Minute(), at.Hour(), dow)
	p.Timezone = scheduleTimezone
	return &p, nil
}

type leadEdit struct {
	Version  int       `json:"version"`
	Name     string    `json:"name"`
	Phone    string    `json:"phone"`
	Purpose  string    `json:"purpose"`
	Tagged   bool      `json:"tagged"`
	Schedule *bool     `json:"schedule,omitempty"`
	CallPlan *CallPlan `json:"call_plan,omitempty"`
	Consent  bool      `json:"outbound_consent"`
}

func (s *Service) registerLeadRoutes(mux *http.ServeMux) {
	s.registerPurposeContent(mux)
	mux.HandleFunc("POST /api/leads/import", s.authed(s.importLeadsCSV))
	mux.HandleFunc("GET /api/leads", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		tag := r.URL.Query().Get("tag")
		s.mu.Lock()
		leads := make([]*Lead, 0, len(s.leads))
		for _, lead := range s.leads {
			if !lead.Deleted && (tag == "" || slices.Contains(lead.Tags, tag)) {
				leads = append(leads, lead)
			}
		}
		s.mu.Unlock()
		sort.Slice(leads, func(i, j int) bool { return leads[i].ID < leads[j].ID })
		writeJSON(w, http.StatusOK, map[string]any{"leads": leads, "outbound_tag": outboundTag, "schedule_tag": scheduleTag, "schedule_timezone": scheduleTimezone})
		return nil
	}))
	mux.HandleFunc("PUT /api/leads/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
		var edit leadEdit
		if err := readJSON(r, &edit); err != nil {
			return err
		}
		lead, err := s.saveLead(r.PathValue("id"), edit, who(r))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"lead": lead, "registration_queued": queuesRegistration(lead)})
		return nil
	}))
}

func (s *Service) saveLead(id string, edit leadEdit, by string) (*Lead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLeadLocked(id, edit, by)
}

func (s *Service) saveLeadLocked(id string, edit leadEdit, by string) (*Lead, error) {
	edit.Name = strings.TrimSpace(edit.Name)
	edit.Phone = strings.TrimSpace(edit.Phone)
	if !leadKey.MatchString(id) || edit.Name == "" || !leadPhone.MatchString(edit.Phone) {
		return nil, fail(http.StatusUnprocessableEntity, "a lead ID, name and phone number of 7–15 digits (optionally starting with +) are required")
	}
	if edit.Purpose != "normal" && !hasBusinessContent(edit.Purpose) {
		return nil, fail(http.StatusUnprocessableEntity, "choose normal call, follow-up, reminder or survey")
	}
	now := time.Now()
	lead := Lead{ID: id, CreatedAt: now, Status: "new"}
	if old := s.leads[id]; old != nil {
		if old.Deleted {
			return nil, ErrNotFound
		}
		lead = *old
	}
	if lead.Version != edit.Version {
		return nil, fail(http.StatusConflict, "this lead has changed; reopen it before saving")
	}
	values := make(map[string]string, len(lead.Values)+3)
	for k, v := range lead.Values {
		values[k] = v
	}
	values["name"], values["phone"], values["purpose"] = edit.Name, edit.Phone, edit.Purpose
	lead.Values = values
	lead.Tags = slices.DeleteFunc(slices.Clone(lead.Tags), func(tag string) bool { return tag == outboundTag || (edit.Schedule != nil && tag == scheduleTag) })
	if edit.Tagged {
		lead.Tags = append(lead.Tags, outboundTag)
	}
	if edit.Schedule != nil && *edit.Schedule {
		lead.Tags = append(lead.Tags, scheduleTag)
	}
	if edit.CallPlan != nil {
		plan, err := edit.CallPlan.normalized()
		if err != nil {
			return nil, err
		}
		if plan != nil && !slices.Contains(lead.Tags, scheduleTag) {
			return nil, fail(http.StatusUnprocessableEntity, "select the schedule tag to set a call time")
		}
		lead.CallPlan = plan
	}
	if !slices.Contains(lead.Tags, scheduleTag) {
		lead.CallPlan = nil
	}
	lead.OutboundConsent = edit.Consent
	lead.Version++
	lead.UpdatedAt = now
	var ticket string
	if queuesRegistration(&lead) {
		var err error
		ticket, err = s.queueRegistration(&lead)
		if err != nil {
			return nil, err
		}
	}
	if _, err := s.events.Append(Event{By: by, Type: "lead.put", Ref: id, Lead: id, LeadRec: &lead}); err != nil {
		if ticket != "" {
			os.Remove(ticket)
		}
		return nil, err
	}
	s.leads[id] = &lead
	return &lead, nil
}
