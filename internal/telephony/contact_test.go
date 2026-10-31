package telephony

import "testing"

func TestContactUserRejectsHeaderAndURIInjection(t *testing.T) {
	for _, value := range []string{"x\r\nVia: bad", "x@elsewhere", "x;transport=tcp", "<sip:x>"} {
		c := testConfig(t)
		c.ContactUser = value
		if c.Validate() == nil {
			t.Fatal("invalid contact user accepted")
		}
	}
}
