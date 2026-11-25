package crm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

const registrationDir = "registration-queue"

var registrationPhone = regexp.MustCompile(`^(?:\+?86)?1[3-9][0-9]{9}$`)

func queuesRegistration(l *Lead) bool {
	return l.CallPlan != nil && l.OutboundConsent && !l.DoNotCall && !l.Deleted &&
		slices.Contains(l.Tags, scheduleTag) && registrationPhone.MatchString(l.Values["phone"])
}

// The caller holds the lead lock through the event append. A woken worker reads
// the committed version over the CRM API before it can register a recipient.
func (s *Service) queueRegistration(l *Lead) (string, error) {
	dir := filepath.Join(s.cfg.Dir, registrationDir)
	f, err := os.CreateTemp(dir, fmt.Sprintf("%s.%d.*.job", l.ID, l.Version))
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err = f.Close(); err == nil {
		var d *os.File
		d, err = os.Open(dir)
		if err == nil {
			err = d.Sync()
			d.Close()
		}
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
