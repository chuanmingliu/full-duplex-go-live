package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Configuration is fixed for the lifetime of a store, so cached vectors always
// belong to the same model and preprocessing settings.
type embeddingClient struct {
	endpoint, model, token      string
	queryPrefix, documentPrefix string
	http                        *http.Client
	name, revision              string
	members                     []*embeddingClient
	health                      embeddingHealth
}

func embeddingClientFromEnv() (*embeddingClient, error) {
	if path := os.Getenv("SKILLS_EMBEDDING_POOL_FILE"); path != "" {
		return embeddingPoolFromFile(path)
	}
	endpoint := os.Getenv("SKILLS_EMBEDDING_URL")
	if endpoint == "" {
		return nil, nil
	}
	return newEmbeddingClient(embeddingSettings{
		URL: endpoint, Model: os.Getenv("SKILLS_EMBEDDING_MODEL"),
		TokenFile:      os.Getenv("SKILLS_EMBEDDING_TOKEN_FILE"),
		QueryPrefix:    os.Getenv("SKILLS_EMBEDDING_QUERY_PREFIX"),
		DocumentPrefix: os.Getenv("SKILLS_EMBEDDING_DOCUMENT_PREFIX"),
	})
}

func newEmbeddingClient(settings embeddingSettings) (*embeddingClient, error) {
	u, err := url.Parse(settings.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("embedding URL must be an HTTP(S) endpoint without credentials, query or fragment")
	}
	if settings.Model == "" {
		return nil, errors.New("embedding model is required")
	}
	var token string
	if settings.TokenFile != "" {
		data, err := os.ReadFile(settings.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read embedding token: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return nil, errors.New("embedding token file is empty")
		}
	}
	return &embeddingClient{
		endpoint: settings.URL, model: settings.Model, token: token,
		name: settings.Name, revision: settings.Revision,
		queryPrefix: settings.QueryPrefix, documentPrefix: settings.DocumentPrefix,
		http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

var errEmbeddingInput = errors.New("embedding endpoint rejected input")

// embed accepts the OpenAI-compatible float embeddings protocol, including
// responses whose data array is returned out of input order.
func (c *embeddingClient) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Encoding string   `json:"encoding_format"`
	}{c.model, inputs, "float"})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid embedding request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("embedding endpoint unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusRequestEntityTooLarge || res.StatusCode == http.StatusUnprocessableEntity {
		return nil, errEmbeddingInput
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding endpoint returned HTTP %d", res.StatusCode)
	}
	const maxResponse = 16 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return nil, errors.New("invalid embedding response size")
	}
	var result struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Data) != len(inputs) {
		return nil, errors.New("invalid embedding response")
	}
	vectors := make([][]float32, len(inputs))
	dimension := 0
	for _, row := range result.Data {
		if row.Index == nil || *row.Index < 0 || *row.Index >= len(inputs) || vectors[*row.Index] != nil {
			return nil, errors.New("invalid embedding response indices")
		}
		v := row.Embedding
		if len(v) == 0 || len(v) > 4096 || (dimension != 0 && len(v) != dimension) {
			return nil, errors.New("invalid embedding dimensions")
		}
		dimension = len(v)
		var squared float64
		for _, value := range v {
			squared += float64(value) * float64(value)
		}
		if squared == 0 || math.IsNaN(squared) || math.IsInf(squared, 0) {
			return nil, errors.New("invalid embedding vector")
		}
		norm := math.Sqrt(squared)
		for i := range v {
			v[i] = float32(float64(v[i]) / norm)
		}
		vectors[*row.Index] = v
	}
	return vectors, nil
}

type semanticIndex struct {
	client     *embeddingClient
	vectors    [][]float32
	dimension  int
	mu         sync.Mutex
	queries    map[[32]byte][]float32
	queryOrder [][32]byte
}

// Build before publishing a revision. Old requests retain their complete index.
// Unchanged descriptions reuse vectors even if bodies or skill ordering change.
func (x *skillIndex) prepareEmbeddings(ctx context.Context, client *embeddingClient, previous *skillIndex) error {
	if client == nil || !x.selection.enabled {
		return nil
	}
	if len(client.members) != 0 {
		return x.prepareEmbeddingPool(ctx, client, previous)
	}
	s := &semanticIndex{client: client, vectors: make([][]float32, len(x.items)), queries: map[[32]byte][]float32{}}
	reusable := map[string][]float32{}
	if previous != nil && previous.semantic != nil && previous.semantic.client == client {
		for i, item := range previous.items {
			if v := previous.semantic.vectors[i]; v != nil {
				reusable[item.skill.Description] = v
			}
		}
	}
	var ids []int
	var inputs []string
	flush := func() error {
		if len(ids) == 0 {
			return nil
		}
		vectors, err := client.embed(ctx, inputs)
		if err != nil {
			return err
		}
		for i, id := range ids {
			s.vectors[id] = vectors[i]
		}
		ids, inputs = nil, nil
		return nil
	}
	for i, item := range x.items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !item.available {
			continue
		}
		if v := reusable[item.skill.Description]; v != nil {
			s.vectors[i] = v
			continue
		}
		ids = append(ids, i)
		inputs = append(inputs, client.documentPrefix+item.skill.Description)
		if len(ids) == 32 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	for _, v := range s.vectors {
		if v == nil {
			continue
		}
		if s.dimension != 0 && len(v) != s.dimension {
			return errors.New("embedding dimensions changed; use a fixed model revision")
		}
		s.dimension = len(v)
	}
	x.semantic = s
	return nil
}

func (s *semanticIndex) query(ctx context.Context, text string) ([]float32, error) {
	key := sha256.Sum256([]byte(text))
	s.mu.Lock()
	v := s.queries[key]
	s.mu.Unlock()
	if v != nil {
		return v, nil
	}
	// A failed or slow query can fall back within the interactive search budget.
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	vectors, err := s.client.embed(ctx, []string{s.client.queryPrefix + text})
	if err != nil {
		return nil, err
	}
	v = vectors[0]
	if len(v) != s.dimension {
		return nil, errors.New("query embedding dimensions differ from catalog")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.queries[key]; !exists {
		if len(s.queryOrder) == 256 {
			delete(s.queries, s.queryOrder[0])
			s.queryOrder = s.queryOrder[1:]
		}
		s.queryOrder = append(s.queryOrder, key)
		s.queries[key] = v
	}
	return v, nil
}
