package journal

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/requestlog"
)

var requestSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var recordedRequestID = regexp.MustCompile(`^llmreq_[a-f0-9]{32}$`)

const requestPageSize = 50

type recordedSession struct {
	ID        string    `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
}

type recordedRequest struct {
	requestlog.Metadata
	Error string `json:"error,omitempty"`
}

func requestPage(r *http.Request, length int) (int, int) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	if offset > length {
		offset = length
	}
	return offset, min(offset+requestPageSize, length)
}

// Root confines every lookup, including symlinks, to the configured recorder.
func (s *Server) openRequests(w http.ResponseWriter) *os.Root {
	if s.cfg.RequestLogDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Request viewing is not configured. Set the Journal -request-log-dir option to the recorder directory."})
		return nil
	}
	root, err := os.OpenRoot(s.cfg.RequestLogDir)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The request recording directory is unavailable."})
		return nil
	}
	return root
}

func requestEntries(root *os.Root, name string) ([]os.DirEntry, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}

func readRequestFile(root *os.Root, name string, limit int64) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("unsupported recording file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("recording exceeds display limit")
	}
	return data, err
}

func readRequestMetadata(root *os.Root, session, id string) recordedRequest {
	row := recordedRequest{Metadata: requestlog.Metadata{ID: id}}
	data, err := readRequestFile(root, filepath.Join(session, id, "metadata.json"), 1<<20)
	if err != nil || json.Unmarshal(data, &row.Metadata) != nil {
		return recordedRequest{Metadata: requestlog.Metadata{ID: id}, Error: "Metadata is unavailable or invalid."}
	}
	// A hashed directory is used by the recorder for session IDs unsuitable for paths.
	key := row.SessionID
	if key == "" {
		key = "unscoped"
	} else if !requestSessionID.MatchString(key) {
		key = fmt.Sprintf("session-%x", sha256.Sum256([]byte(key)))
	}
	if row.ID != id || key != session {
		return recordedRequest{Metadata: requestlog.Metadata{ID: id}, Error: "Recording identity does not match its directory."}
	}
	return row
}

func (s *Server) handleRequestSessions(w http.ResponseWriter, r *http.Request) {
	root := s.openRequests(w)
	if root == nil {
		return
	}
	defer root.Close()
	entries, err := requestEntries(root, ".")
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Unable to list recorded sessions."})
		return
	}
	rows := []recordedSession{}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	for _, entry := range entries {
		if !entry.IsDir() || !requestSessionID.MatchString(entry.Name()) || !strings.Contains(entry.Name(), query) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		rows = append(rows, recordedSession{ID: entry.Name(), UpdatedAt: info.ModTime().UTC()})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	start, end := requestPage(r, len(rows))
	writeJSON(w, 200, map[string]any{"sessions": rows[start:end], "total": len(rows), "offset": start, "more": end < len(rows)})
}

func (s *Server) handleSessionRequests(w http.ResponseWriter, r *http.Request) {
	session := r.PathValue("session")
	if !requestSessionID.MatchString(session) {
		writeJSON(w, 400, map[string]string{"error": "Invalid session ID."})
		return
	}
	root := s.openRequests(w)
	if root == nil {
		return
	}
	defer root.Close()
	entries, err := requestEntries(root, session)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "No recordings found for this session."})
		return
	}
	rows := []recordedRequest{}
	for _, entry := range entries {
		if entry.IsDir() && recordedRequestID.MatchString(entry.Name()) {
			rows = append(rows, readRequestMetadata(root, session, entry.Name()))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	start, end := requestPage(r, len(rows))
	writeJSON(w, 200, map[string]any{"session_id": session, "requests": rows[start:end], "total": len(rows), "offset": start, "more": end < len(rows)})
}

func (s *Server) handleRecordedRequest(w http.ResponseWriter, r *http.Request) {
	session, id := r.PathValue("session"), r.PathValue("request")
	if !requestSessionID.MatchString(session) || !recordedRequestID.MatchString(id) {
		writeJSON(w, 400, map[string]string{"error": "Invalid recording ID."})
		return
	}
	root := s.openRequests(w)
	if root == nil {
		return
	}
	defer root.Close()
	row := readRequestMetadata(root, session, id)
	if row.Error != "" {
		writeJSON(w, 404, map[string]string{"error": row.Error})
		return
	}
	raw, err := readRequestFile(root, filepath.Join(session, id, "request.json"), 64<<20)
	if err != nil {
		writeJSON(w, 422, map[string]string{"error": "Request body is unavailable or exceeds the 64 MiB display limit."})
		return
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	writeJSON(w, 200, map[string]any{"metadata": row.Metadata, "raw_request": string(raw), "integrity_ok": digest == row.RequestSHA256 && len(raw) == row.RequestBytes})
}
