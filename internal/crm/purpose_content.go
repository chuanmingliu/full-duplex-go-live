package crm

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type purposeContent struct {
	Content string `json:"content"`
}

func hasBusinessContent(purpose string) bool {
	switch purpose {
	case "follow-up", "reminder", "survey":
		return true
	}
	return false
}

func (s *Service) registerPurposeContent(mux *http.ServeMux) {
	for _, method := range []string{"GET", "PUT"} {
		mux.HandleFunc(method+" /api/call-purposes/{purpose}", s.authed(func(w http.ResponseWriter, r *http.Request) error {
			purpose := r.PathValue("purpose")
			if !hasBusinessContent(purpose) {
				return fail(http.StatusNotFound, "unknown call purpose")
			}
			key := "call-purpose-" + purpose
			var content purposeContent
			if r.Method == http.MethodPut {
				var edit struct {
					Content *string `json:"content"`
				}
				if err := readJSON(r, &edit); err != nil {
					return err
				}
				if edit.Content == nil {
					return fail(http.StatusBadRequest, "content must be text")
				}
				content.Content = strings.TrimSpace(*edit.Content)
				raw, err := json.Marshal(content)
				if err != nil {
					return err
				}
				if _, err := s.configs.Save(key, raw, "scheduled call business content", who(r)); err != nil {
					return err
				}
			} else {
				v, err := s.configs.Get(key, 0)
				if err == nil {
					if err := json.Unmarshal(v.Body, &content); err != nil {
						return err
					}
				} else if !errors.Is(err, ErrNotFound) {
					return err
				}
			}
			writeJSON(w, http.StatusOK, content)
			return nil
		}))
	}
}
