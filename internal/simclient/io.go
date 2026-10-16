// Package simclient provides bounded, authenticated receiving-run clients.
package simclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"
)

const MaxArtifactBytes = 1 << 20

// StrictJSON rejects duplicate keys at every depth, unknown fields and trailing data.
func StrictJSON(raw []byte, value any) error {
	if !utf8.Valid(raw) || len(raw) > MaxArtifactBytes {
		return errors.New("invalid or oversized JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return err
					}
					key := k.(string)
					if seen[key] {
						return errors.New("duplicate JSON field")
					}
					seen[key] = true
					if err = walk(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := walk(); err != nil {
						return err
					}
				}
			default:
				return errors.New("invalid JSON delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return errors.New("invalid JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid JSON fields")
	}
	return exactFields(raw, reflect.TypeOf(value))
}

// encoding/json accepts case-insensitive struct fields; external artifacts do not.
func exactFields(raw []byte, t reflect.Type) error {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) || reflect.PointerTo(t).Implements(reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil
		}
		known := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			known[name] = f.Type
		}
		for k, v := range fields {
			ft, ok := known[k]
			if !ok {
				return errors.New("unknown JSON field")
			}
			if err := exactFields(v, ft); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return nil
		}
		for _, v := range values {
			if err := exactFields(v, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return nil
		}
		for _, v := range values {
			if err := exactFields(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open input artifact")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxArtifactBytes+1))
	if err != nil || len(raw) > MaxArtifactBytes {
		return nil, errors.New("cannot read bounded input artifact")
	}
	return raw, nil
}

// Endpoint accepts HTTPS, and HTTP only on a literal loopback address or localhost.
func Endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("invalid endpoint")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host == "" || (u.Scheme == "http" && host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return nil, errors.New("endpoint requires HTTPS outside loopback")
	}
	if strings.ContainsAny(raw, "\r\n\t") {
		return nil, errors.New("invalid endpoint")
	}
	return u, nil
}

// WritePrivate atomically creates or replaces a JSON artifact with mode 0600.
// New directories use 0700. Symlink destinations are replaced, never followed.
func WritePrivate(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(raw) > MaxArtifactBytes {
		return errors.New("cannot encode bounded artifact")
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create artifact directory")
	}
	f, err := os.CreateTemp(dir, ".simctl-")
	if err != nil {
		return errors.New("cannot create artifact")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot write artifact")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot save artifact")
	}
	return nil
}

func SafeID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
