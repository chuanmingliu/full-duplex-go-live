package deepseek

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/chuanmingliu/golive/internal/requestlog"
)

func (l *LLM) recordRequest(ctx context.Context, body []byte, model string, ln *lane) *requestlog.Record {
	if l.Requests == nil {
		return nil
	}
	endpoint, _ := url.Parse(completionsURL(l.BaseURL))
	endpoint.User = nil
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	record, err := l.Requests.Begin(requestlog.ScopeFrom(ctx), requestlog.Metadata{Provider: l.Name(), Model: model, Endpoint: endpoint.String(), Lane: ln.n, KeySlot: ln.keyN + 1}, body)
	if err != nil {
		slog.Error("llm: request recording failed", "session", requestlog.ScopeFrom(ctx).SessionID, "error_type", "storage_error")
		return nil
	}
	slog.Debug("llm: request recorded", "request_id", record.ID(), "session", requestlog.ScopeFrom(ctx).SessionID)
	return record
}
func finishRecording(record *requestlog.Record, status int, providerID, outcome string) {
	if err := record.Finish(status, providerID, outcome); err != nil {
		slog.Error("llm: request recording completion failed", "request_id", record.ID(), "error_type", "storage_error")
	}
}
