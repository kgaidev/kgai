package engine

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Contextual search: the primary caller is an AI that passes free text ("how is
// invoice rendering structured", a task description, a diff summary) — not exact
// words. So instead of substring matching, queries and documents are tokenized
// (with camelCase/kebab-case splitting), stopwords dropped, and documents ranked by
// IDF-weighted token overlap with fuzzy matching (exact > prefix > small typo >
// substring) across weighted fields (name > kind > rationale/props). The corpus is
// the live graph — small by design — so scoring in-process is well under 50ms even
// at tens of thousands of decisions.

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "to": true, "in": true, "on": true,
	"for": true, "and": true, "or": true, "is": true, "are": true, "be": true,
	"with": true, "that": true, "this": true, "it": true, "as": true, "at": true,
	"by": true, "from": true, "we": true, "was": true, "were": true, "how": true,
	"what": true, "why": true, "does": true, "do": true, "should": true, "must": true,
	"our": true, "its": true, "into": true, "about": true,
}

// tokenize splits free text into normalized search tokens: word boundaries on any
// non-alphanumeric rune AND on camelCase humps, lowercased, stopwords removed.
func tokenize(s string) []string {
	var toks []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 {
			t := strings.ToLower(string(cur))
			if !stopwords[t] {
				toks = append(toks, t)
			}
		}
		cur = cur[:0]
	}
	var prev rune
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && unicode.IsLower(prev) {
				flush() // camelCase boundary
			}
			cur = append(cur, r)
		default:
			flush()
		}
		prev = r
	}
	flush()
	return toks
}

// matchStrength grades how well a query token matches a document token.
func matchStrength(q, d string) float64 {
	if q == d {
		return 1.0
	}
	// Common-prefix ratio = poor-man's stemming, both directions: "invoicing" ~
	// "invoice", "invo" ~ "invoice", "rendering" ~ "render". Essential for free-text
	// AI queries where word forms rarely match the stored names exactly.
	if lcp := commonPrefixLen(q, d); lcp >= 3 {
		longer := len(q)
		if len(d) > longer {
			longer = len(d)
		}
		if ratio := float64(lcp) / float64(longer); ratio >= 0.55 {
			return 0.4 + 0.4*ratio // 0.62 .. 0.8
		}
	}
	if len(q) >= 4 && strings.Contains(d, q) {
		return 0.4
	}
	if len(q) >= 4 && len(d) >= 4 && editDistanceAtMost1(q, d) {
		return 0.5
	}
	return 0
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// editDistanceAtMost1 reports whether two strings are within one edit
// (substitution, insertion or deletion) of each other — enough to absorb typos.
func editDistanceAtMost1(a, b string) bool {
	la, lb := len(a), len(b)
	if la > lb {
		a, b, la, lb = b, a, lb, la
	}
	if lb-la > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < la && j < lb {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if la == lb {
			i++
		}
		j++
	}
	return edits+(lb-j)+(la-i) <= 1
}

// searchField is one weighted text field of a document.
type searchField struct {
	weight float64
	tokens []string
}

// searchDoc is a scorable unit (an element or a decision).
type searchDoc struct {
	hit    SearchHit
	fields []searchField
}

// scoreDocs ranks documents against a free-text query. Token weights use IDF so
// terms that appear everywhere contribute little; per token the best field match
// counts (field weight × match strength).
func scoreDocs(query string, docs []searchDoc) []SearchHit {
	qToks := tokenize(query)
	if len(qToks) == 0 {
		return nil
	}
	// document frequency per query token (exact matches only)
	df := map[string]int{}
	for _, d := range docs {
		seen := map[string]bool{}
		for _, f := range d.fields {
			for _, t := range f.tokens {
				seen[t] = true
			}
		}
		for _, q := range qToks {
			if seen[q] {
				df[q]++
			}
		}
	}
	n := float64(len(docs))
	idf := func(q string) float64 { return 1 + math.Log(n/(1+float64(df[q]))) }

	type scored struct {
		hit   SearchHit
		score float64
	}
	var out []scored
	for _, d := range docs {
		total := 0.0
		matched := 0
		for _, q := range qToks {
			best := 0.0
			for _, f := range d.fields {
				for _, t := range f.tokens {
					if s := matchStrength(q, t) * f.weight; s > best {
						best = s
					}
				}
			}
			if best > 0 {
				matched++
				total += best * idf(q)
			}
		}
		if total <= 0 {
			continue
		}
		// favour documents matching more distinct query terms
		total *= 1 + 0.3*float64(matched-1)
		h := d.hit
		h.Score = total
		out = append(out, scored{h, total})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	hits := make([]SearchHit, len(out))
	for i, s := range out {
		hits[i] = s.hit
	}
	return hits
}

// elementDoc builds the scorable view of an element.
func elementDoc(id, kind, name, props string, extra string) searchDoc {
	return searchDoc{
		hit: SearchHit{Kind: "element", ID: id, Name: name, Extra: extra},
		fields: []searchField{
			{3.0, tokenize(name)},
			{2.0, tokenize(kind)},
			{1.0, tokenize(props)},
		},
	}
}

// aboutScore ranks a single element against free text (used by `kg context --about`).
// Returns 0..1-ish (normalized by the best possible single-field weight).
func aboutScore(query, kind, name, props string) float64 {
	qToks := tokenize(query)
	if len(qToks) == 0 {
		return 0
	}
	fields := []searchField{{3.0, tokenize(name)}, {2.0, tokenize(kind)}, {1.0, tokenize(props)}}
	total := 0.0
	for _, q := range qToks {
		best := 0.0
		for _, f := range fields {
			for _, t := range f.tokens {
				if s := matchStrength(q, t) * f.weight; s > best {
					best = s
				}
			}
		}
		total += best
	}
	return total / (3.0 * float64(len(qToks)))
}
