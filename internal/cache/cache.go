// Package cache implements the semantic response cache.
//
// Lookup is two-tier, cheapest first:
//
//  1. Exact-hash fast path — a SHA-256 over the normalized messages, model, and the
//     params that change the answer. A direct HGET; zero false positives, no
//     embedding cost. This handles the common "same prompt again" case.
//
//  2. Semantic path — only on an exact miss. Embed the normalized prompt and run a
//     Redis vector KNN (cosine) scoped to the model. Reuse the nearest entry only if
//     similarity >= the route threshold.
//
// Callers gate eligibility (route opt-in + low temperature) before calling in; this
// package is purely mechanism. Redis uses the COSINE distance metric, so vectors do
// not need to be pre-normalized — similarity is scale-invariant.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rishabht877/PromptRelay/internal/types"
)

const keyPrefix = "prcache:"

// Cache is a Redis-backed semantic cache. Store entries are hashes at
// prcache:<hash> with fields: model (TAG), embedding (VECTOR), response (JSON).
type Cache struct {
	rdb   *redis.Client
	index string
	dim   int
}

func New(rdb *redis.Client, index string, dim int) *Cache {
	return &Cache{rdb: rdb, index: index, dim: dim}
}

// EnsureIndex creates the vector index if it does not already exist. The vector
// dimension is fixed at creation time to the embedding model's width — changing the
// embedding model later requires dropping and rebuilding this index.
func (c *Cache) EnsureIndex(ctx context.Context) error {
	err := c.rdb.FTCreate(ctx, c.index,
		&redis.FTCreateOptions{
			OnHash: true,
			Prefix: []any{keyPrefix},
		},
		&redis.FieldSchema{
			FieldName: "model",
			FieldType: redis.SearchFieldTypeTag,
		},
		&redis.FieldSchema{
			FieldName: "embedding",
			FieldType: redis.SearchFieldTypeVector,
			VectorArgs: &redis.FTVectorArgs{
				FlatOptions: &redis.FTFlatOptions{
					Type:           "FLOAT32",
					Dim:            c.dim,
					DistanceMetric: "COSINE",
				},
			},
		},
	).Err()
	if err != nil && !strings.Contains(err.Error(), "Index already exists") {
		return fmt.Errorf("create vector index: %w", err)
	}
	return nil
}

// GetExact returns a cached response for an exact key, or ok=false on a miss.
func (c *Cache) GetExact(ctx context.Context, key string) (types.ChatResponse, bool, error) {
	val, err := c.rdb.HGet(ctx, keyPrefix+key, "response").Result()
	if err == redis.Nil {
		return types.ChatResponse{}, false, nil
	}
	if err != nil {
		return types.ChatResponse{}, false, err
	}
	var resp types.ChatResponse
	if err := json.Unmarshal([]byte(val), &resp); err != nil {
		return types.ChatResponse{}, false, err
	}
	return resp, true, nil
}

// SearchSemantic finds the nearest cached response for the given model and returns
// it only if cosine similarity >= threshold. The returned similarity is reported
// regardless, for logging.
func (c *Cache) SearchSemantic(ctx context.Context, model string, embedding []float32, threshold float64) (types.ChatResponse, float64, bool, error) {
	query := fmt.Sprintf("(@model:{%s})=>[KNN 1 @embedding $vec AS score]", escapeTag(model))
	res, err := c.rdb.FTSearchWithArgs(ctx, c.index, query, &redis.FTSearchOptions{
		Return: []redis.FTSearchReturn{
			{FieldName: "response"},
			{FieldName: "score"},
		},
		SortBy:         []redis.FTSearchSortBy{{FieldName: "score", Asc: true}},
		DialectVersion: 2,
		Params:         map[string]any{"vec": float32sToBytes(embedding)},
		LimitOffset:    0,
		Limit:          1,
	}).Result()
	if err != nil {
		return types.ChatResponse{}, 0, false, err
	}
	if res.Total == 0 || len(res.Docs) == 0 {
		return types.ChatResponse{}, 0, false, nil
	}

	doc := res.Docs[0]
	distStr, ok := doc.Fields["score"]
	if !ok {
		return types.ChatResponse{}, 0, false, nil
	}
	dist, err := strconv.ParseFloat(distStr, 64)
	if err != nil {
		return types.ChatResponse{}, 0, false, fmt.Errorf("parse cosine distance: %w", err)
	}
	similarity := 1 - dist // COSINE metric returns distance = 1 - cosine similarity
	if similarity < threshold {
		return types.ChatResponse{}, similarity, false, nil
	}

	respStr, ok := doc.Fields["response"]
	if !ok {
		return types.ChatResponse{}, similarity, false, nil
	}
	var resp types.ChatResponse
	if err := json.Unmarshal([]byte(respStr), &resp); err != nil {
		return types.ChatResponse{}, similarity, false, err
	}
	return resp, similarity, true, nil
}

// Store writes a response under its exact key along with the embedding used for
// semantic search, expiring after ttl.
func (c *Cache) Store(ctx context.Context, key, model string, embedding []float32, resp types.ChatResponse, ttl time.Duration) error {
	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	fullKey := keyPrefix + key
	if err := c.rdb.HSet(ctx, fullKey, map[string]any{
		"model":     model,
		"embedding": float32sToBytes(embedding),
		"response":  string(payload),
	}).Err(); err != nil {
		return err
	}
	if ttl > 0 {
		c.rdb.Expire(ctx, fullKey, ttl)
	}
	return nil
}

// --- key derivation & helpers ---

// Normalize produces the canonical string used for both the exact hash and the
// embedding: role-prefixed messages with whitespace collapsed.
func Normalize(msgs []types.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.ToLower(strings.TrimSpace(m.Role)))
		b.WriteString(": ")
		b.WriteString(collapseWS(m.Content))
	}
	return b.String()
}

// ExactKey is the SHA-256 hex over model + normalized messages + answer-affecting
// params.
func ExactKey(req types.ChatRequest, normalized string) string {
	h := sha256.New()
	io.WriteString(h, req.Model)
	io.WriteString(h, "\x00")
	io.WriteString(h, normalized)
	io.WriteString(h, "\x00")
	io.WriteString(h, paramSignature(req))
	return hex.EncodeToString(h.Sum(nil))
}

func paramSignature(req types.ChatRequest) string {
	t := "nil"
	if req.Temperature != nil {
		t = strconv.FormatFloat(*req.Temperature, 'f', -1, 64)
	}
	m := "nil"
	if req.MaxTokens != nil {
		m = strconv.Itoa(*req.MaxTokens)
	}
	p := "nil"
	if req.TopP != nil {
		p = strconv.FormatFloat(*req.TopP, 'f', -1, 64)
	}
	return fmt.Sprintf("t=%s;m=%s;p=%s;stop=%s", t, m, p, strings.Join(req.Stop, "|"))
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// escapeTag backslash-escapes characters RediSearch treats as special inside a TAG
// filter, so model ids like "gpt-4o-mini" match literally.
func escapeTag(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func float32sToBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}
