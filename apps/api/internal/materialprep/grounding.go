package materialprep

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Claim-support validation (M4). Cited fact and answer ids prove the
// writer named real inputs; they do not prove the drafted sentences
// are supported by them. Every drafted sentence is therefore checked
// against the evidence text behind its citations:
//
//   - Sentences carrying a personal claim — a number or a credential
//     keyword — must reproduce every number and mid-sentence name
//     exactly from the cited owner evidence (career source bodies,
//     cited answer texts, answered clarifications) and share at
//     least two distinctive tokens with it. Vacancy text never
//     grounds an owner claim, so "10 years" fails against a vacancy
//     asking for ten.
//   - Other sentences may only name mid-sentence capitalized terms
//     the combined vacancy and owner evidence actually contains, so
//     greetings, role titles and neutral subjects pass while
//     invented names, places and employers hold.
//   - Sentences with no marker pass: neutral prose without a
//     personal claim legitimately needs no owner-fact reference.
//
// The check is deterministic token grounding, not semantic
// understanding: paraphrases that keep their numbers, names and two
// distinctive tokens pass; unsupported puffery without numbers,
// credential words or new names passes; sentence-initial
// capitalization is grammatical rather than evidential, so an
// unsupported name escapes only in first position. Number words
// normalize both ways ("6" matches "six") and simple plurals meet
// their singulars. A Jev semantic assessment remains the follow-up
// for genuinely ambiguous prose; unsupported concrete claims hold
// today.

var groundingStopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "but": {}, "if": {},
	"then": {}, "than": {}, "so": {}, "as": {}, "at": {}, "by": {},
	"for": {}, "from": {}, "in": {}, "into": {}, "of": {}, "off": {},
	"on": {}, "onto": {}, "out": {}, "over": {}, "to": {}, "up": {},
	"with": {}, "within": {}, "without": {}, "about": {}, "above": {},
	"across": {}, "after": {}, "against": {}, "along": {}, "among": {},
	"around": {}, "before": {}, "behind": {}, "below": {}, "beneath": {},
	"beside": {}, "between": {}, "beyond": {}, "during": {}, "except": {},
	"through": {}, "toward": {}, "under": {}, "until": {}, "upon": {},
	"while": {}, "is": {}, "am": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {},
	"having": {}, "do": {}, "does": {}, "did": {}, "doing": {}, "will": {},
	"would": {}, "shall": {}, "should": {}, "may": {}, "might": {},
	"must": {}, "can": {}, "could": {}, "i": {}, "me": {}, "my": {},
	"mine": {}, "you": {}, "your": {}, "yours": {}, "he": {}, "him": {},
	"his": {}, "she": {}, "her": {}, "hers": {}, "it": {}, "its": {},
	"we": {}, "us": {}, "our": {}, "ours": {}, "they": {}, "them": {},
	"their": {}, "theirs": {}, "this": {}, "that": {}, "these": {},
	"those": {}, "there": {}, "here": {}, "what": {}, "which": {},
	"who": {}, "whom": {}, "whose": {}, "when": {}, "where": {},
	"why": {}, "how": {}, "all": {}, "any": {}, "both": {}, "each": {},
	"every": {}, "few": {}, "more": {}, "most": {}, "other": {},
	"some": {}, "such": {}, "only": {}, "own": {}, "same": {},
	"very": {}, "just": {}, "also": {}, "even": {}, "still": {},
	"yet": {}, "already": {}, "please": {}, "well": {}, "much": {},
	"many": {}, "little": {}, "less": {}, "least": {}, "several": {},
	"dear": {}, "hello": {}, "hi": {}, "hey": {}, "greetings": {},
	"regards": {}, "sincerely": {}, "faithfully": {}, "truly": {},
	"best": {}, "kind": {}, "warm": {}, "thank": {}, "thanks": {},
	"hiring": {}, "manager": {}, "managers": {}, "team": {},
	"sir": {}, "madam": {}, "madame": {}, "mr": {}, "ms": {},
	"mrs": {}, "dr": {},
	"de": {}, "het": {}, "een": {}, "van": {}, "en": {}, "ik": {},
	"je": {}, "u": {}, "met": {}, "voor": {}, "dat": {}, "die": {},
	"wat": {}, "op": {}, "te": {}, "aan": {}, "zijn": {}, "heb": {},
	"heeft": {}, "hebben": {}, "mijn": {}, "jouw": {}, "onze": {},
	"hun": {}, "naar": {}, "als": {}, "maar": {}, "door": {},
	"onder": {}, "tussen": {}, "zonder": {}, "ook": {}, "niet": {},
	"geen": {}, "wel": {}, "nog": {}, "al": {}, "er": {}, "hier": {},
	"daar": {}, "waar": {}, "wanneer": {}, "hoe": {}, "waarom": {},
	"wie": {}, "welke": {}, "deze": {}, "alle": {}, "elke": {},
	"veel": {}, "andere": {}, "sommige": {}, "zo": {}, "dus": {},
	"toch": {}, "graag": {}, "beste": {}, "geachte": {}, "heer": {},
	"mevrouw": {},
}

// credentialKeywords mark sentences that claim verifiable owner facts:
// quantities, schooling, credentials, languages, leadership and
// shipped work. Transmittal and motive prose ("please find my CV",
// "I am excited") carries none of these and validates as neutral.
var credentialKeywords = map[string]struct{}{
	"year": {}, "years": {}, "jaar": {}, "jaren": {},
	"degree": {}, "degrees": {}, "bachelor": {}, "bachelors": {},
	"master": {}, "masters": {}, "mba": {}, "phd": {},
	"doctorate": {}, "university": {}, "universities": {},
	"college": {}, "hogeschool": {}, "universiteit": {},
	"certificate": {}, "certificates": {}, "certified": {},
	"certification": {}, "certifications": {}, "diploma": {},
	"licensed": {}, "licence": {}, "license": {}, "gpa": {},
	"fluent": {}, "fluency": {}, "native": {}, "bilingual": {},
	"managed": {}, "manages": {}, "managing": {}, "management": {},
	"mentor": {}, "mentors": {}, "mentoring": {}, "mentored": {},
	"coach": {}, "coaches": {}, "coaching": {}, "coached": {},
	"led": {}, "lead": {}, "leads": {}, "leading": {},
	"built": {}, "build": {}, "builds": {}, "shipped": {},
	"ship": {}, "founded": {}, "designed": {}, "design": {},
	"published": {}, "publish": {}, "award": {}, "awards": {},
	"awarded": {}, "patent": {}, "patents": {}, "clearance": {},
	"senior": {}, "medior": {}, "junior": {}, "principal": {},
	"staff": {}, "honors": {}, "honours": {},
}

// genreWords are application-genre terms any draft may name without
// evidence; they carry no personal claim.
var genreWords = map[string]struct{}{
	"cv": {}, "resume": {}, "curriculum": {}, "vitae": {},
	"cover": {}, "letter": {}, "letters": {}, "motivation": {},
	"motivational": {}, "motivatiebrief": {}, "sollicitatiebrief": {},
	"email": {}, "application": {}, "applications": {},
	"portfolio": {}, "attachment": {}, "attachments": {},
	"attached": {}, "enclosed": {},
}

var numberWords = map[string]string{
	"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4",
	"five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
	"ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13",
	"fourteen": "14", "fifteen": "15", "sixteen": "16",
	"seventeen": "17", "eighteen": "18", "nineteen": "19",
	"twenty": "20", "thirty": "30", "forty": "40", "fifty": "50",
	"sixty": "60", "seventy": "70", "eighty": "80", "ninety": "90",
}

var digitWords = map[string]string{}

func init() {
	for word, digit := range numberWords {
		if _, seen := digitWords[digit]; !seen {
			digitWords[digit] = word
		}
	}
}

// groundingToken is one normalized token with its source markers.
// initial marks the sentence's first word: its capitalization is
// grammatical, but its digits still claim a quantity.
type groundingToken struct {
	text        string
	capitalized bool
	hasDigit    bool
	initial     bool
}

// groundingTokens splits text on non-letters/digits, drops stopwords
// and single letters, and normalizes numbers both ways so "6" and
// "six" meet, plus simple plurals so "Mondays" meets "Monday".
// Capitalization is read before lowercasing: a token is capitalized
// when its first rune is an upper-case letter.
func groundingTokens(text string) []groundingToken {
	raw := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]groundingToken, 0, len(raw))
	for index, word := range raw {
		if word == "" {
			continue
		}
		lowered := strings.ToLower(word)
		if _, stop := groundingStopwords[lowered]; stop {
			continue
		}
		hasDigit := strings.ContainsAny(word, "0123456789")
		if !hasDigit && len([]rune(word)) < 2 {
			continue
		}
		first, _ := utf8.DecodeRuneInString(word)
		initial := index == 0
		token := groundingToken{text: lowered, hasDigit: hasDigit,
			capitalized: unicode.IsUpper(first), initial: initial}
		out = append(out, token)
		if twin, ok := numberWords[lowered]; ok {
			out = append(out, groundingToken{text: twin, hasDigit: true, initial: initial})
		} else if twin, ok := digitWords[lowered]; ok && isDigits(lowered) {
			out = append(out, groundingToken{text: twin, initial: initial})
		} else if singular, ok := singularTwin(lowered); ok {
			out = append(out, groundingToken{text: singular, initial: initial})
		}
	}
	return out
}

// singularTwin strips one trailing "s" from longer words so simple
// plurals meet their singulars on both sides of the comparison.
func singularTwin(lowered string) (string, bool) {
	if len(lowered) <= 4 || !strings.HasSuffix(lowered, "s") || strings.HasSuffix(lowered, "ss") {
		return "", false
	}
	return strings.TrimSuffix(lowered, "s"), true
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// groundingSet is a normalized token corpus.
type groundingSet map[string]struct{}

func corpusOf(texts ...string) groundingSet {
	out := groundingSet{}
	for _, text := range texts {
		for _, token := range groundingTokens(text) {
			out[token.text] = struct{}{}
		}
	}
	return out
}

// splitSentences splits drafted content into checkable clauses on
// blank lines, terminal punctuation, commas and subordinate
// openers, so a supported lead cannot smuggle an unsupported
// relative clause ("Go engineer who mentors juniors") through one
// shared-token budget.
func splitSentences(content string) []string {
	parts := strings.FieldsFunc(content, func(r rune) bool {
		return r == '\n' || r == '.' || r == '!' || r == '?' || r == ';' || r == ','
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		for _, clause := range splitSubordinate(part) {
			if trimmed := strings.TrimSpace(clause); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

// splitSubordinate splits one part on lowercase subordinate openers.
// Conjunctions (and/but/or) stay intact: splitting them would shred
// ordinary compounds like "research and rationale".
func splitSubordinate(part string) []string {
	out := []string{part}
	for _, opener := range []string{" who ", " which ", " that "} {
		var next []string
		for _, piece := range out {
			next = append(next, strings.Split(piece, opener)...)
		}
		out = next
	}
	return out
}

// evidentialName reports whether one token names something the
// evidence must contain: a mid-sentence capitalized term of
// substance, outside the genre vocabulary.
func evidentialName(token groundingToken) bool {
	if !token.capitalized || token.initial || len(token.text) < 3 {
		return false
	}
	_, genre := genreWords[token.text]
	return !genre
}

// unsupportedSentence returns the first sentence the evidence cannot
// support, or "" when every sentence is grounded. owner carries the
// cited owner evidence (career bodies, cited answers, answered
// clarifications); role carries the vacancy context (title, company,
// description, requirements, document labels, destination).
// Personal-claim sentences must reproduce every number and
// mid-sentence name from the owner evidence and share two
// distinctive tokens with it; other sentences only need their
// mid-sentence names present in either corpus.
func unsupportedSentence(content string, owner, role groundingSet) string {
	for _, sentence := range splitSentences(content) {
		tokens := groundingTokens(sentence)
		if len(tokens) == 0 {
			continue
		}
		claim := false
		for _, token := range tokens {
			if token.hasDigit {
				claim = true
				break
			}
			if _, ok := credentialKeywords[token.text]; ok {
				claim = true
				break
			}
		}
		distinctive := map[string]struct{}{}
		for _, token := range tokens {
			if _, genre := genreWords[token.text]; genre {
				continue
			}
			distinctive[token.text] = struct{}{}
		}
		if claim {
			for _, token := range tokens {
				if _, genre := genreWords[token.text]; genre {
					continue
				}
				if token.hasDigit {
					if _, ok := owner[token.text]; !ok {
						return sentence
					}
					continue
				}
				if evidentialName(token) {
					if _, ok := owner[token.text]; !ok {
						return sentence
					}
				}
			}
			need := 2
			if len(distinctive) < need {
				need = len(distinctive)
			}
			shared := 0
			for token := range distinctive {
				if _, ok := owner[token]; ok {
					shared++
				}
			}
			if shared < need {
				return sentence
			}
			continue
		}
		for _, token := range tokens {
			if !evidentialName(token) {
				continue
			}
			if _, ok := owner[token.text]; ok {
				continue
			}
			if _, ok := role[token.text]; ok {
				continue
			}
			return sentence
		}
	}
	return ""
}
