package memory

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// minTokenRunes drops one-rune Latin noise ("a", "1") while keeping CJK
	// bigrams, which are produced separately below.
	minTokenRunes = 2
	// maxTokens bounds both the stored keyword set and the query token set. It
	// caps the ranker at maxTokens*items comparisons per request and stops a
	// pathological paste from writing a keyword list larger than its content.
	maxTokens = 64
)

// stopwords covers only words that appear in almost every English sentence.
// Domain words a QEMU user would search for ("reset", "value", "device") are
// deliberately absent: removing them would make the most useful queries empty.
var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "that": {}, "this": {}, "from": {},
	"are": {}, "was": {}, "were": {}, "you": {}, "your": {}, "not": {}, "but": {},
	"has": {}, "have": {}, "had": {}, "its": {}, "it's": {}, "into": {}, "onto": {},
	"can": {}, "will": {}, "would": {}, "should": {}, "there": {}, "then": {},
	"than": {}, "when": {}, "what": {}, "which": {}, "who": {}, "how": {},
	"please": {}, "about": {}, "also": {}, "just": {}, "any": {}, "all": {},
}

// ExtractKeywords is the indexing side of retrieval and tokenize is the query
// side. They are the same function on purpose: two different tokenizers would
// silently stop matching each other the first time either one changed.
func ExtractKeywords(content string) []string {
	return tokenize(content)
}

// tokenize returns a sorted, deduplicated token set. Sorting makes a stored
// keyword list byte-identical across runs, which is what lets a memory file be
// diffed and a test assert on it.
func tokenize(text string) []string {
	normalized := strings.ToLower(normalizeText(text))
	if normalized == "" {
		return nil
	}
	seen := make(map[string]struct{}, maxTokens)
	ordered := make([]string, 0, maxTokens)
	add := func(token string) bool {
		if _, exists := seen[token]; exists {
			return true
		}
		if len(ordered) >= maxTokens {
			return false
		}
		seen[token] = struct{}{}
		ordered = append(ordered, token)
		return true
	}
	fields := strings.FieldsFunc(normalized, isSeparator)
	for _, field := range fields {
		// A CJK run carries no spaces, so the whole clause arrives as one field
		// and would only ever match a query repeating it verbatim. Bigrams give
		// it the same partial-match behaviour Latin words get for free.
		if containsIdeograph(field) {
			if !addIdeographBigrams(field, add) {
				break
			}
			continue
		}
		if utf8.RuneCountInString(field) < minTokenRunes {
			continue
		}
		if _, stop := stopwords[field]; stop {
			continue
		}
		if !add(field) {
			break
		}
	}
	// The cap keeps the head of the text (usually the most specific part) and
	// the sort happens after truncation, so the result is deterministic without
	// letting alphabetical order decide what survives.
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return ordered
}

func isSeparator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-'
}

func isIdeograph(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

func containsIdeograph(field string) bool {
	for _, r := range field {
		if isIdeograph(r) {
			return true
		}
	}
	return false
}

// addIdeographBigrams emits overlapping pairs of adjacent ideographs and keeps
// any embedded Latin/digit run as its own token, so "PL011串口寄存器" indexes
// both "pl011" and the Chinese bigrams. It returns false once the token cap is
// reached so the caller stops scanning.
func addIdeographBigrams(field string, add func(string) bool) bool {
	runes := []rune(field)
	var latin []rune
	flush := func() bool {
		if len(latin) < minTokenRunes {
			latin = latin[:0]
			return true
		}
		token := string(latin)
		latin = latin[:0]
		if _, stop := stopwords[token]; stop {
			return true
		}
		return add(token)
	}
	for index := 0; index < len(runes); index++ {
		if !isIdeograph(runes[index]) {
			latin = append(latin, runes[index])
			continue
		}
		if !flush() {
			return false
		}
		if index+1 < len(runes) && isIdeograph(runes[index+1]) {
			if !add(string(runes[index : index+2])) {
				return false
			}
			continue
		}
		// A lone ideograph is a real word in Chinese and Japanese, so it is
		// indexed even though it is below the Latin minimum length.
		if !add(string(runes[index])) {
			return false
		}
	}
	return flush()
}

func tokenSet(tokens []string) map[string]struct{} {
	if len(tokens) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		set[token] = struct{}{}
	}
	return set
}

// intersect returns the matched terms in the item's stored order, which is
// already sorted, so two runs of the same query report the same terms.
func intersect(query map[string]struct{}, keywords []string) []string {
	if len(query) == 0 || len(keywords) == 0 {
		return nil
	}
	matched := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if _, ok := query[keyword]; ok {
			matched = append(matched, keyword)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return matched
}
