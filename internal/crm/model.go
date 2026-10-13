// Package crm keeps the leads a phone assistant calls: who they are, what is
// known about their vehicle and policy, every contact made with them and what
// came of it, and when they are to be called next. What it holds is rendered
// into the prompt of each call.
//
// It is one process with its state in memory and its history on disk: every
// change is an event appended to a checksummed log before it takes effect, so
// the log is at once the storage, the audit trail and the backup. See store.go.
package crm

import (
	"time"
)

// Variable is one entry of the catalogue: something that can be known about a
// lead and written into a prompt as {{ key }}.
type Variable struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Group string `json:"group"`
	// Type is how a value is checked and shown: text, number, money, date,
	// datetime, enum, bool, phone, plate.
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"` // for enum
	// Source says where the value comes from: "lead" is kept on the lead and
	// edited by hand or imported; "summary" is taken from the summary of the
	// latest contact that has it; "computed" is worked out when a prompt is
	// rendered and cannot be edited.
	Source   string `json:"source"`
	Required bool   `json:"required,omitempty"`
	// Default is what a prompt gets when the lead has no value.
	Default string `json:"default,omitempty"`
	// Personal marks a value as personal data: masked in lists, left out of
	// the operational log, and its export recorded.
	Personal bool   `json:"personal,omitempty"`
	Help     string `json:"help,omitempty"`
	Example  string `json:"example,omitempty"`
	Order    int    `json:"order"`
	// Builtin variables can be relabelled and reordered but not removed: the
	// page and the scheduler read them.
	Builtin bool `json:"builtin,omitempty"`
}

// Lead is one person to be reached.
type Lead struct {
	ID string `json:"id"`
	// Version counts saves. An edit names the version it was made from, and
	// is refused if the lead has been saved since.
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Deleted   bool      `json:"deleted,omitempty"`
	// Status is where the lead stands: new, contacted, interested, quoted,
	// won, lost, unreachable.
	Status    string `json:"status"`
	DoNotCall bool   `json:"do_not_call,omitempty"`
	// Values holds the lead's own variables, by key, as text. A date is
	// 2006-01-02, a bool "true" or "false", a number or money plain digits.
	Values map[string]string `json:"values"`
	// Import is the import that created or last changed the lead, if one did.
	Import string `json:"import,omitempty"`
}

// Contact is one reach to a lead, by any channel, and what came of it.
type Contact struct {
	ID      string    `json:"id"`
	LeadID  string    `json:"lead_id"`
	At      time.Time `json:"at"`
	Channel string    `json:"channel"` // phone, wechat, other
	By      string    `json:"by"`      // an operator, or "assistant"
	// Outcome: no_answer, talked, callback, wechat_added, not_interested,
	// do_not_call, failed.
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
	// Items are the summary's points, by summary-variable key.
	Items map[string]string `json:"items,omitempty"`
	// Reviewed is false for a summary a model drafted that nobody has looked
	// at. It is used either way; the page says which it is.
	Reviewed  bool    `json:"reviewed"`
	DraftedBy string  `json:"drafted_by,omitempty"` // "model" or an operator
	CallID    string  `json:"call_id,omitempty"`
	DurationS float64 `json:"duration_s,omitempty"`
	Booking   string  `json:"booking,omitempty"`
	// Render names the prompt the call ran on: renders/<sha>.txt.
	Render   string `json:"render,omitempty"`
	Template string `json:"template,omitempty"` // "T0001 v3"
	Deleted  bool   `json:"deleted,omitempty"`
}

// Template is a prompt with Jinja2 in it. Saving one makes a new version;
// nothing is ever changed in place, so a call can always say exactly which
// text it was rendered from.
type Template struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	// SHA names the text: the first twelve hex digits of its SHA-256.
	SHA   string `json:"sha"`
	Chars int    `json:"chars"`
	// Body is the template itself. It is kept in a file of its own and left
	// out of the event that records the save.
	Body string `json:"body,omitempty"`
	Note string `json:"note,omitempty"`
	// Names are the variables the template reads, in the order it first
	// reads them, and StaticChars how many characters come before the first
	// thing that can differ between renders: what a model's prompt cache can
	// keep from one lead to the next.
	Names       []string  `json:"names"`
	StaticChars int       `json:"static_chars"`
	CreatedAt   time.Time `json:"created_at"`
	By          string    `json:"by"`
}

// CallRecord is one call placed with a rendered prompt.
type CallRecord struct {
	At       time.Time `json:"at"`
	Template string    `json:"template"`
	Version  int       `json:"version"`
	// Render names the exact prompt the call ran on: renders/<sha>.txt.
	Render string `json:"render"`
	Chars  int    `json:"chars"`
	// Vars are the names of the variables supplied. Their values are in the
	// rendered prompt, which is kept as privately as the leads.
	Vars          []string `json:"vars"`
	Undefined     []string `json:"undefined,omitempty"`
	GreetingChars int      `json:"greeting_chars"`
	Lead          string   `json:"lead,omitempty"`
	// CallID is the originator's name for the call: the journal's too.
	CallID string `json:"call_id,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// Booking is a call, or a reminder, set for a time.
type Booking struct {
	ID     string    `json:"id"`
	LeadID string    `json:"lead_id"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // call, reminder
	// Status: pending, dialing, in_call, done, no_answer, failed, missed,
	// cancelled.
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	// Attempts is how many times it has been dialled, and NextAt when the
	// next try is due: At to begin with, later after a call nobody answered.
	Attempts  int       `json:"attempts"`
	NextAt    time.Time `json:"next_at"`
	CallIDs   []string  `json:"call_ids,omitempty"`
	Reason    string    `json:"reason,omitempty"` // why it failed, was missed or is waiting
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	UpdatedAt time.Time `json:"updated_at"`
	// From is the contact whose summary proposed this booking, if one did.
	From string `json:"from,omitempty"`
}

// Settings are the scheduler's rules and the few things that are the same for
// every lead.
type Settings struct {
	// CallFrom and CallTo bound the hours the scheduler will dial in, as
	// "09:00" and "20:00" in Timezone.
	CallFrom string `json:"call_from"`
	CallTo   string `json:"call_to"`
	Timezone string `json:"timezone"`
	// Retries is how many more times a call nobody answered is tried, RetryGapMin
	// minutes apart; MaxPerLeadPerDay bounds dials to one lead in a day,
	// whoever asked for them.
	Retries          int `json:"retries"`
	RetryGapMin      int `json:"retry_gap_min"`
	MaxPerLeadPerDay int `json:"max_per_lead_per_day"`
	// MissedAfterMin is how late a booking may run after the scheduler was
	// down before it is marked missed instead.
	MissedAfterMin int `json:"missed_after_min"`
	// Paused stops the scheduler dialling. Bookings stay.
	Paused bool `json:"paused"`
	// ActiveTemplate is the template calls use.
	ActiveTemplate string `json:"active_template"`
	// HistoryN is how many past summaries {{ history }} holds, and HistoryChars
	// the most it may run to.
	HistoryN     int `json:"history_n"`
	HistoryChars int `json:"history_chars"`
}

// DefaultSettings is what a new store starts with.
func DefaultSettings() Settings {
	return Settings{CallFrom: "09:00", CallTo: "20:00", Timezone: "Asia/Shanghai",
		Retries: 2, RetryGapMin: 30, MaxPerLeadPerDay: 3, MissedAfterMin: 60,
		HistoryN: 3, HistoryChars: 600}
}

// Statuses a lead can have, in the order a list shows them.
var LeadStatuses = []string{"new", "contacted", "interested", "quoted", "won", "lost", "unreachable"}

// Outcomes a contact can have.
var ContactOutcomes = []string{"talked", "callback", "wechat_added", "no_answer", "not_interested", "do_not_call", "failed"}

// Channels a contact can be made by. The store takes any of them from any
// writer: a service that talks to leads another way records its contacts here.
var Channels = []string{"phone", "wechat", "other"}

// State is everything the store holds, as the events so far have made it.
type State struct {
	Variables map[string]*Variable   `json:"variables"`
	Leads     map[string]*Lead       `json:"leads"`
	Contacts  map[string]*Contact    `json:"contacts"`
	Templates map[string][]*Template `json:"templates"` // by name, versions in order
	Bookings  map[string]*Booking    `json:"bookings"`
	Settings  Settings               `json:"settings"`
	// Next is the next number to give out, by kind of thing: L, C, T, B, I.
	Next map[string]int `json:"next"`
}

func newState() *State {
	return &State{Variables: map[string]*Variable{}, Leads: map[string]*Lead{}, Contacts: map[string]*Contact{},
		Templates: map[string][]*Template{}, Bookings: map[string]*Booking{}, Settings: DefaultSettings(), Next: map[string]int{}}
}

// Change is one field of a thing before and after an event, for the audit
// trail. A personal value is kept in the log, which is as private as the leads
// themselves, and masked where the trail is shown in a list.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Event is one change, as the log keeps it.
type Event struct {
	Seq int64     `json:"seq"`
	At  time.Time `json:"at"`
	By  string    `json:"by"`
	// Type is what happened: lead.put, lead.delete, variable.put,
	// variable.delete, contact.put, template.put, booking.put, settings.put,
	// and — for the trail only, changing nothing — import.done, export.done,
	// call.placed, auth.login, auth.refused.
	Type string `json:"type"`
	// Ref is the thing it happened to, and Lead the lead it concerns.
	Ref  string `json:"ref,omitempty"`
	Lead string `json:"lead,omitempty"`
	// Batch ties together the events of one import or one bulk edit.
	Batch   string   `json:"batch,omitempty"`
	Note    string   `json:"note,omitempty"`
	Changes []Change `json:"changes,omitempty"`
	// Exactly one of these holds what the thing is after the event.
	Variable *Variable   `json:"variable,omitempty"`
	LeadRec  *Lead       `json:"lead_rec,omitempty"`
	Contact  *Contact    `json:"contact,omitempty"`
	Template *Template   `json:"template,omitempty"`
	Booking  *Booking    `json:"booking,omitempty"`
	Settings *Settings   `json:"settings,omitempty"`
	Call     *CallRecord `json:"call,omitempty"`
	// Chain is a checksum of this event and every one before it: a line
	// changed, lost or put in by hand breaks it from there on.
	Chain string `json:"chain"`
}
