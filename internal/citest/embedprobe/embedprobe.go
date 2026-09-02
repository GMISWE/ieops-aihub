// Package embedprobe provides a deterministic, offline embedding provider whose
// cosine similarities against a probe query are KNOWN CONSTANTS.
//
// aihub#148 needs to assert a similarity FLOOR — "0.99 returns nothing, 0.9
// returns exactly the rows above it" — from two different test packages
// (internal/server for hops 3+4, internal/mcp for hops 1→4). A hash-derived fake
// like internal/domain's stubEmbedProvider cannot support that: its directions
// are arbitrary, so the only assertions available are inequalities against
// whatever it happened to produce, and "the filter worked" becomes
// indistinguishable from "the filter matched everything".
//
// It lives in one package rather than being copied into each test package
// because the numbers below ARE the contract those tests assert. Two copies
// could drift, and a drifted copy would still be internally consistent — the
// tests would keep passing while measuring different floors.
//
//	query text (no marker) -> e0                    baseline direction
//	text containing Near   -> e0                    cosine 1.0
//	text containing Far    -> 0.5·e0 + √0.75·e1     cosine 0.5
//
// A floor of 0.99 therefore admits Near and excludes Far; 0.9 does the same;
// 0.4 admits both. Those three make "took effect", "fell back" and "matched
// everything" three distinguishable outcomes.
//
// This is NOT a NoopProvider, so domain.isNoopProvider reports false and
// domain.Recall routes to the vector path — the only path on which a cosine
// floor exists at all.
package embedprobe

import (
	"context"
	"math"
	"strings"
)

// Dims is small on purpose: these probes assert a floor, never ranking quality.
const Dims = 8

// Marker substrings a seeded body carries to choose its direction.
const (
	Near = "NEARMATCH" // cosine 1.0 against a marker-free query
	Far  = "FARMATCH"  // cosine 0.5 against a marker-free query
)

// Cosines the markers produce, so a test can state its expectation in terms of
// this contract instead of restating the arithmetic.
const (
	NearCosine = 1.0
	FarCosine  = 0.5
)

// Provider is the deterministic provider described in the package doc.
type Provider struct{}

func (p *Provider) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, Dims)
	switch {
	case strings.Contains(text, Near):
		vec[0] = 1
	case strings.Contains(text, Far):
		vec[0] = 0.5
		vec[1] = float32(math.Sqrt(0.75))
	default:
		// Query strings land here, and define the reference direction.
		vec[0] = 1
	}
	return vec, nil
}

func (p *Provider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := p.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (p *Provider) ModelID() string              { return "embedprobe-v1" }
func (p *Provider) Dims() int                    { return Dims }
func (p *Provider) Ping(_ context.Context) error { return nil }
