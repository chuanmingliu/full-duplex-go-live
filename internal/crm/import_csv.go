package crm

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const maxCSVBytes = 2 << 20
const maxCSVRows = 1000

func (s *Service) importLeadsCSV(w http.ResponseWriter, r *http.Request) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCSVBytes+1))
	if err != nil {
		return fail(400, "could not read CSV")
	}
	if len(data) > maxCSVBytes {
		return fail(413, "CSV exceeds 2 MiB")
	}
	if !utf8.Valid(data) {
		return fail(422, "save the CSV as UTF-8")
	}
	edits, err := parseLeadCSV(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if err != nil {
		return fail(422, "%v", err)
	}
	created, skipped, queued := 0, 0, 0
	by := who(r)
	s.mu.Lock()
	seen := make(map[string]bool, len(s.leads))
	for _, lead := range s.leads {
		if !lead.Deleted {
			seen[importPhoneKey(lead.Values["phone"])] = true
		}
	}
	for _, edit := range edits {
		phone := importPhoneKey(edit.Phone)
		if seen[phone] {
			skipped++
			continue
		}
		var lead *Lead
		lead, err = s.saveLeadLocked(rand.Text(), edit, by)
		if err != nil {
			break
		}
		seen[phone] = true
		created++
		if queuesRegistration(lead) {
			queued++
		}
	}
	s.mu.Unlock()
	if err != nil {
		s.log.Error("crm: CSV import stopped", "created", created, "err", err)
		return fail(500, "import stopped after %d new leads; re-upload to resume", created)
	}
	writeJSON(w, http.StatusOK, map[string]int{"created": created, "skipped": skipped, "registration_queued": queued})
	return nil
}

func importPhoneKey(phone string) string {
	if registrationPhone.MatchString(phone) {
		return "+86" + phone[len(phone)-11:]
	}
	return phone
}

func parseLeadCSV(data []byte) ([]leadEdit, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("expected a CSV header with name and phone")
	}
	columns := make(map[string]int, len(header))
	for i, col := range header {
		col = strings.ToLower(strings.TrimSpace(col))
		switch col {
		case "name", "phone", "purpose", "call_time", "repeat_days", "outbound_consent":
		default:
			return nil, fmt.Errorf("unknown column %q", col)
		}
		if _, found := columns[col]; found {
			return nil, fmt.Errorf("duplicate column %q", col)
		}
		columns[col] = i
	}
	for _, key := range []string{"name", "phone"} {
		if _, found := columns[key]; !found {
			return nil, fmt.Errorf("missing column %q", key)
		}
	}
	var edits []leadEdit
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid CSV: %w", err)
		}
		if len(edits) >= maxCSVRows {
			return nil, fmt.Errorf("upload at most %d leads per file", maxCSVRows)
		}
		get := func(key string) string {
			if i, ok := columns[key]; ok {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		edit, err := parseLeadRow(get)
		if err != nil {
			line, _ := reader.FieldPos(0)
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		edits = append(edits, edit)
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("the CSV contains no leads")
	}
	return edits, nil
}

func parseLeadRow(get func(string) string) (leadEdit, error) {
	edit := leadEdit{Name: get("name"), Phone: get("phone"), Purpose: strings.ToLower(get("purpose")), Tagged: true}
	if edit.Name == "" || !leadPhone.MatchString(edit.Phone) {
		return edit, fmt.Errorf("enter a name and a phone number of 7–15 digits, optionally starting with +")
	}
	switch edit.Purpose {
	case "":
		edit.Purpose = "follow-up"
	case "normal call":
		edit.Purpose = "normal"
	}
	if edit.Purpose != "normal" && !hasBusinessContent(edit.Purpose) {
		return edit, fmt.Errorf("unknown call purpose")
	}
	switch strings.ToLower(get("outbound_consent")) {
	case "true", "yes", "1":
		edit.Consent = true
	case "", "false", "no", "0":
	default:
		return edit, fmt.Errorf("outbound_consent must be true or false")
	}
	at, repeat := get("call_time"), get("repeat_days")
	if at == "" && repeat == "" {
		return edit, nil
	}
	if at == "" || repeat == "" {
		return edit, fmt.Errorf("supply both call_time and repeat_days")
	}
	plan := CallPlan{Time: at}
	for _, day := range strings.FieldsFunc(strings.ToLower(repeat), func(r rune) bool { return r == '|' || r == ',' || r == ' ' || r == ';' }) {
		idx, ok := map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}[day]
		if !ok {
			return edit, fmt.Errorf("use repeat days such as Mon|Wed|Fri")
		}
		plan.Days = append(plan.Days, idx)
	}
	normalized, err := plan.normalized()
	if err != nil {
		return edit, err
	}
	enabled := true
	edit.Schedule = &enabled
	edit.CallPlan = normalized
	return edit, nil
}
