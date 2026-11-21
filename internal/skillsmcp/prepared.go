package skillsmcp

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const turnPreparationExtension = "ai.liuchuanming.golive/turn-preparation"

type preparedTurn struct {
	Version   int `json:"version"`
	Resources []struct {
		Manifest Entry  `json:"manifest"`
		MIME     string `json:"mimeType"`
		Text     string `json:"text"`
	} `json:"resources"`
	Retrieval         string `json:"retrieval"`
	EmbeddingProvider string `json:"embedding_provider"`
	EmbeddingRevision string `json:"embedding_revision"`
	ContextTruncated  bool   `json:"context_truncated"`
	FallbackReason    string `json:"fallback_reason"`
}

func verifyGuidance(expected File, entry Entry, uri, mime, text string) (Guidance, error) {
	if entry.URI != expected.URI || len(entry.Resources) != 1 || entry.Resources[0] != expected {
		return Guidance{}, errors.New("skill changed during retrieval")
	}
	if uri != expected.URI || mime != "text/markdown" || len(text) != expected.Size || fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))) != expected.Digest {
		return Guidance{}, errors.New("skill integrity check failed")
	}
	head, body, ok := strings.Cut(text, "\n---\n")
	lines := strings.Split(head, "\n")
	if !ok || len(lines) != 3 || lines[0] != "---" {
		return Guidance{}, errors.New("unsupported skill frontmatter")
	}
	front := map[string]string{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ": ")
		var decoded string
		if !ok || json.Unmarshal([]byte(value), &decoded) != nil {
			return Guidance{}, errors.New("invalid skill frontmatter")
		}
		front[key] = decoded
	}
	if len(front) != 2 || front["name"] != entry.Frontmatter["name"] || front["description"] != entry.Frontmatter["description"] {
		return Guidance{}, errors.New("skill frontmatter mismatch")
	}
	return Guidance{URI: uri, Digest: expected.Digest, Text: strings.TrimSpace(body), State: entry.Meta[bindingKey].State}, nil
}

func verifyPrepared(selected selection, prepared *preparedTurn, trace *Trace) ([]Guidance, error) {
	if prepared.Version != 1 || len(prepared.Resources) != len(selected.Messages) {
		return nil, errors.New("invalid prepared skill response")
	}
	if trace != nil {
		trace.Retrieval = prepared.Retrieval
		trace.EmbeddingProvider = prepared.EmbeddingProvider
		trace.EmbeddingRevision = prepared.EmbeddingRevision
		trace.FallbackReason = prepared.FallbackReason
		trace.ContextTruncated = prepared.ContextTruncated
	}
	out := []Guidance{}
	seen := map[string]bool{}
	for i, resource := range prepared.Resources {
		link := selected.Messages[i].Content
		var expected File
		if link.Type != "resource_link" || !strings.HasPrefix(link.URI, "skill://") || seen[link.URI] || json.Unmarshal(link.Meta[routeKey], &expected) != nil || expected.URI != link.URI {
			return nil, errors.New("invalid prepared skill manifest")
		}
		seen[link.URI] = true
		trace.Begin("verify prepared skill", link.URI, time.Now())
		guidance, err := verifyGuidance(expected, resource.Manifest, resource.Manifest.URI, resource.MIME, resource.Text)
		if err != nil {
			return nil, err
		}
		if trace != nil {
			trace.Steps[len(trace.Steps)-1].Digest = expected.Digest
		}
		out = append(out, guidance)
	}
	return out, nil
}
