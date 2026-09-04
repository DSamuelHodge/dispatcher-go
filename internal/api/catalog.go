package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Verb detail levels for progressive disclosure (Code Mode-style discovery).
const (
	DetailNames   = "names"
	DetailSummary = "summary" // default for GET /v1/verbs
	DetailFull    = "full"
)

func normalizeDetail(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "", DetailSummary:
		return DetailSummary
	case DetailNames, "name":
		return DetailNames
	case DetailFull, "all":
		return DetailFull
	default:
		return ""
	}
}

func verbSummaryLine(v verbs.Verb) string {
	parts := []string{string(v.Tier)}
	if v.StdinArg != nil {
		parts = append(parts, "stdin:"+v.StdinArg.Arg)
	}
	if len(v.Args) > 0 {
		names := make([]string, 0, len(v.Args))
		for _, a := range v.Args {
			n := a.Name
			if a.Required {
				n += "*"
			}
			names = append(names, n)
		}
		parts = append(parts, "args:"+strings.Join(names, ","))
	}
	if isStreamVerb(v) {
		parts = append(parts, "stream")
	}
	if len(v.Argv) > 0 {
		parts = append(parts, v.Argv[0])
	}
	return strings.Join(parts, " · ")
}

func isStreamVerb(v verbs.Verb) bool {
	w, ok := v.Watch.(map[string]any)
	if !ok {
		return false
	}
	m, _ := w["mode"].(string)
	return m == "stream"
}

func verbNamesOnly(cat *verbs.Catalog) []string {
	out := make([]string, 0, len(cat.Order))
	out = append(out, cat.Order...)
	return out
}

func verbSummaryEntry(v verbs.Verb) map[string]any {
	return map[string]any{
		"name":    v.Name,
		"tier":    v.Tier,
		"summary": verbSummaryLine(v),
		"stream":  isStreamVerb(v),
	}
}

func verbFullEntry(v verbs.Verb) map[string]any {
	m := map[string]any{
		"name":   v.Name,
		"tier":   v.Tier,
		"argv":   v.Argv,
		"parser": v.Parser,
		"watch":  v.Watch,
	}
	if len(v.Args) > 0 {
		args := make([]map[string]any, 0, len(v.Args))
		for _, a := range v.Args {
			args = append(args, map[string]any{
				"name":     a.Name,
				"flag":     a.Flag,
				"type":     a.Type,
				"required": a.Required,
			})
		}
		m["args"] = args
	}
	if v.StdinArg != nil {
		m["stdin_arg"] = map[string]any{"arg": v.StdinArg.Arg}
	}
	if v.TimeoutS > 0 {
		m["timeout_s"] = v.TimeoutS
	}
	if v.Retries != nil {
		m["retries"] = *v.Retries
	}
	if v.CircuitBreakerThreshold != nil {
		m["circuit_breaker_threshold"] = *v.CircuitBreakerThreshold
	}
	return m
}

func listVerbsDetail(cat *verbs.Catalog, detail string) (any, error) {
	d := normalizeDetail(detail)
	if d == "" {
		return nil, fmt.Errorf("detail must be names|summary|full")
	}
	switch d {
	case DetailNames:
		return map[string]any{
			"detail": DetailNames,
			"count":  len(cat.Order),
			"verbs":  verbNamesOnly(cat),
		}, nil
	case DetailSummary:
		list := make([]map[string]any, 0, len(cat.Order))
		for _, name := range cat.Order {
			list = append(list, verbSummaryEntry(cat.ByName[name]))
		}
		return map[string]any{
			"detail": DetailSummary,
			"count":  len(list),
			"verbs":  list,
		}, nil
	default: // full
		list := make([]map[string]any, 0, len(cat.Order))
		for _, name := range cat.Order {
			list = append(list, verbFullEntry(cat.ByName[name]))
		}
		return map[string]any{
			"detail": DetailFull,
			"count":  len(list),
			"verbs":  list,
		}, nil
	}
}

type verbSearchHit struct {
	Name    string `json:"name"`
	Tier    string `json:"tier"`
	Summary string `json:"summary"`
	Score   int    `json:"score"`
	Stream  bool   `json:"stream"`
}

func searchVerbs(cat *verbs.Catalog, query string, limit int) map[string]any {
	q := strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		limit = 50
	}
	if q == "" {
		// empty query → first N summaries in catalog order
		hits := make([]verbSearchHit, 0, limit)
		for _, name := range cat.Order {
			if len(hits) >= limit {
				break
			}
			v := cat.ByName[name]
			hits = append(hits, verbSearchHit{
				Name: v.Name, Tier: string(v.Tier), Summary: verbSummaryLine(v),
				Score: 0, Stream: isStreamVerb(v),
			})
		}
		return map[string]any{
			"query": query, "limit": limit, "count": len(hits),
			"total": len(cat.Order), "hits": hits,
		}
	}
	tokens := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '.' || r == '_' || r == '-' || r == '/'
	})
	type scored struct {
		hit verbSearchHit
	}
	var scoredHits []scored
	for _, name := range cat.Order {
		v := cat.ByName[name]
		score := scoreVerb(v, q, tokens)
		if score <= 0 {
			continue
		}
		scoredHits = append(scoredHits, scored{hit: verbSearchHit{
			Name: v.Name, Tier: string(v.Tier), Summary: verbSummaryLine(v),
			Score: score, Stream: isStreamVerb(v),
		}})
	}
	sort.SliceStable(scoredHits, func(i, j int) bool {
		if scoredHits[i].hit.Score != scoredHits[j].hit.Score {
			return scoredHits[i].hit.Score > scoredHits[j].hit.Score
		}
		return scoredHits[i].hit.Name < scoredHits[j].hit.Name
	})
	if len(scoredHits) > limit {
		scoredHits = scoredHits[:limit]
	}
	hits := make([]verbSearchHit, len(scoredHits))
	for i := range scoredHits {
		hits[i] = scoredHits[i].hit
	}
	return map[string]any{
		"query": query, "limit": limit, "count": len(hits),
		"total": len(cat.Order), "hits": hits,
	}
}

func scoreVerb(v verbs.Verb, q string, tokens []string) int {
	name := strings.ToLower(v.Name)
	score := 0
	if name == q {
		score += 100
	}
	if strings.HasPrefix(name, q) {
		score += 40
	}
	if strings.Contains(name, q) {
		score += 25
	}
	hay := name
	if len(v.Argv) > 0 {
		bin := strings.ToLower(v.Argv[0])
		hay += " " + bin
		hay += " " + strings.TrimPrefix(bin, "termux-")
	}
	for _, a := range v.Args {
		hay += " " + strings.ToLower(a.Name)
	}
	if v.StdinArg != nil {
		hay += " " + strings.ToLower(v.StdinArg.Arg)
	}
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if strings.Contains(name, tok) {
			score += 15
		} else if strings.Contains(hay, tok) {
			score += 8
		}
	}
	return score
}
