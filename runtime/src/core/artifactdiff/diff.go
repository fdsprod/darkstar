// Package artifactdiff produces deterministic, bounded line-oriented diffs.
// It is deliberately independent of artifact storage and disclosure policy.
package artifactdiff

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	AlgorithmVersion = "darkstar-line-dp/v1"
	ContextLines     = 3
	MaxInputBytes    = 2 << 20
	MaxWorkUnits     = 1_000_000
	DefaultPageSize  = 100
	MaxPageSize      = 200
	MaxPageBytes     = 3 << 20
)

var ErrBudgetExceeded = errors.New("diff generation budget exceeded")

type Policy struct {
	Algorithm     string `json:"algorithm"`
	ContextLines  int    `json:"contextLines"`
	MaxInputBytes int64  `json:"maxInputBytes"`
	MaxWorkUnits  int64  `json:"maxWorkUnits"`
	PageSize      int    `json:"pageSize"`
	MaxPageBytes  int    `json:"maxPageBytes"`
}

type Entry struct {
	Kind         string `json:"kind"`
	FromLine     uint64 `json:"fromLine,omitempty"`
	ToLine       uint64 `json:"toLine,omitempty"`
	Text         string `json:"text,omitempty"`
	OmittedLines uint64 `json:"omittedLines,omitempty"`
}

func (value Entry) MarshalJSON() ([]byte, error) {
	switch value.Kind {
	case "unchanged":
		if value.FromLine == 0 || value.ToLine == 0 || value.OmittedLines != 0 {
			return nil, errors.New("unchanged diff entry is invalid")
		}
		return json.Marshal(struct {
			Kind     string `json:"kind"`
			FromLine uint64 `json:"fromLine"`
			ToLine   uint64 `json:"toLine"`
			Text     string `json:"text"`
		}{value.Kind, value.FromLine, value.ToLine, value.Text})
	case "removed":
		if value.FromLine == 0 || value.ToLine != 0 || value.OmittedLines != 0 {
			return nil, errors.New("removed diff entry is invalid")
		}
		return json.Marshal(struct {
			Kind     string `json:"kind"`
			FromLine uint64 `json:"fromLine"`
			Text     string `json:"text"`
		}{value.Kind, value.FromLine, value.Text})
	case "added":
		if value.FromLine != 0 || value.ToLine == 0 || value.OmittedLines != 0 {
			return nil, errors.New("added diff entry is invalid")
		}
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			ToLine uint64 `json:"toLine"`
			Text   string `json:"text"`
		}{value.Kind, value.ToLine, value.Text})
	case "omitted":
		if value.FromLine == 0 || value.ToLine == 0 || value.OmittedLines == 0 || value.Text != "" {
			return nil, errors.New("omitted diff entry is invalid")
		}
		return json.Marshal(struct {
			Kind         string `json:"kind"`
			FromLine     uint64 `json:"fromLine"`
			ToLine       uint64 `json:"toLine"`
			OmittedLines uint64 `json:"omittedLines"`
		}{value.Kind, value.FromLine, value.ToLine, value.OmittedLines})
	default:
		return nil, errors.New("invalid diff entry kind")
	}
}

func (value *Entry) UnmarshalJSON(content []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(content, &tag); err != nil {
		return err
	}
	decode := func(destination any) error {
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(destination); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("expected one diff entry")
		}
		return nil
	}
	switch tag.Kind {
	case "unchanged":
		var decoded struct {
			Kind     string  `json:"kind"`
			FromLine uint64  `json:"fromLine"`
			ToLine   uint64  `json:"toLine"`
			Text     *string `json:"text"`
		}
		if err := decode(&decoded); err != nil {
			return err
		}
		if decoded.FromLine == 0 || decoded.ToLine == 0 || decoded.Text == nil {
			return errors.New("unchanged diff entry requires both line numbers and text")
		}
		*value = Entry{Kind: decoded.Kind, FromLine: decoded.FromLine, ToLine: decoded.ToLine, Text: *decoded.Text}
	case "removed":
		var decoded struct {
			Kind     string  `json:"kind"`
			FromLine uint64  `json:"fromLine"`
			Text     *string `json:"text"`
		}
		if err := decode(&decoded); err != nil {
			return err
		}
		if decoded.FromLine == 0 || decoded.Text == nil {
			return errors.New("removed diff entry requires fromLine and text")
		}
		*value = Entry{Kind: decoded.Kind, FromLine: decoded.FromLine, Text: *decoded.Text}
	case "added":
		var decoded struct {
			Kind   string  `json:"kind"`
			ToLine uint64  `json:"toLine"`
			Text   *string `json:"text"`
		}
		if err := decode(&decoded); err != nil {
			return err
		}
		if decoded.ToLine == 0 || decoded.Text == nil {
			return errors.New("added diff entry requires toLine and text")
		}
		*value = Entry{Kind: decoded.Kind, ToLine: decoded.ToLine, Text: *decoded.Text}
	case "omitted":
		var decoded struct {
			Kind         string `json:"kind"`
			FromLine     uint64 `json:"fromLine"`
			ToLine       uint64 `json:"toLine"`
			OmittedLines uint64 `json:"omittedLines"`
		}
		if err := decode(&decoded); err != nil {
			return err
		}
		if decoded.FromLine == 0 || decoded.ToLine == 0 || decoded.OmittedLines == 0 {
			return errors.New("omitted diff entry is incomplete")
		}
		*value = Entry{Kind: decoded.Kind, FromLine: decoded.FromLine, ToLine: decoded.ToLine, OmittedLines: decoded.OmittedLines}
	default:
		return errors.New("invalid diff entry kind")
	}
	return nil
}

type Hunk struct {
	FromStart uint64  `json:"fromStart"`
	ToStart   uint64  `json:"toStart"`
	Entries   []Entry `json:"entries"`
}

type Page struct {
	Hunks      []Hunk `json:"hunks"`
	NextCursor string `json:"nextCursor,omitempty"`
	Total      int    `json:"totalEntries"`
}

type op struct {
	kind string
	text string
}

func Generate(left, right []byte, binding, cursor string, requestedLimit int) (Page, Policy, string, string, error) {
	limit := requestedLimit
	if limit == 0 {
		limit = DefaultPageSize
	}
	policy := Policy{Algorithm: AlgorithmVersion, ContextLines: ContextLines, MaxInputBytes: MaxInputBytes, MaxWorkUnits: MaxWorkUnits, PageSize: limit, MaxPageBytes: MaxPageBytes}
	policyDigest := digestJSON(policy)
	cursorBinding := binding + "|" + policyDigest
	if limit < 1 || limit > MaxPageSize || len(left) > MaxInputBytes || len(right) > MaxInputBytes {
		return Page{}, policy, policyDigest, "", ErrBudgetExceeded
	}
	leftLines, rightLines := splitLines(string(left)), splitLines(string(right))
	ops, err := calculate(leftLines, rightLines)
	if err != nil {
		return Page{}, policy, policyDigest, "", err
	}
	entries := compact(ops)
	resultDigest := digestJSON(entries)
	offset, err := decodeCursor(cursor, cursorBinding, len(entries))
	if err != nil {
		return Page{}, policy, policyDigest, resultDigest, err
	}
	end := min(offset+limit, len(entries))
	page := Page{Hunks: hunks(entries[offset:end]), Total: len(entries)}
	for end > offset {
		encoded, marshalErr := json.Marshal(page)
		if marshalErr != nil {
			return Page{}, policy, policyDigest, resultDigest, marshalErr
		}
		if len(encoded) <= MaxPageBytes {
			break
		}
		end--
		page.Hunks = hunks(entries[offset:end])
	}
	if end == offset && offset < len(entries) {
		return Page{}, policy, policyDigest, resultDigest, ErrBudgetExceeded
	}
	if end < len(entries) {
		page.NextCursor = encodeCursor(cursorBinding, end)
	}
	return page, policy, policyDigest, resultDigest, nil
}

func digestJSON(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func splitLines(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func calculate(left, right []string) ([]op, error) {
	prefix := 0
	for prefix < len(left) && prefix < len(right) && left[prefix] == right[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(left)-prefix && suffix < len(right)-prefix && left[len(left)-1-suffix] == right[len(right)-1-suffix] {
		suffix++
	}
	a, b := left[prefix:len(left)-suffix], right[prefix:len(right)-suffix]
	if int64(len(a))*int64(len(b)) > MaxWorkUnits {
		return nil, ErrBudgetExceeded
	}
	dp := make([][]uint32, len(a)+1)
	for i := range dp {
		dp[i] = make([]uint32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] { // stable tie: removal first
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	result := make([]op, 0, len(left)+len(right))
	for _, line := range left[:prefix] {
		result = append(result, op{"unchanged", line})
	}
	for i, j := 0, 0; i < len(a) || j < len(b); {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			result = append(result, op{"unchanged", a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || dp[i+1][j] >= dp[i][j+1]):
			result = append(result, op{"removed", a[i]})
			i++
		default:
			result = append(result, op{"added", b[j]})
			j++
		}
	}
	for _, line := range left[len(left)-suffix:] {
		result = append(result, op{"unchanged", line})
	}
	return result, nil
}

func compact(ops []op) []Entry {
	entries := make([]Entry, 0, len(ops))
	var fromLine, toLine uint64 = 1, 1
	for start := 0; start < len(ops); {
		end := start + 1
		for end < len(ops) && ops[end].kind == ops[start].kind {
			end++
		}
		if ops[start].kind == "unchanged" && end-start > ContextLines*2 {
			for _, value := range ops[start : start+ContextLines] {
				entries = append(entries, lineEntry(value, &fromLine, &toLine))
			}
			omitted := uint64(end - start - ContextLines*2)
			entries = append(entries, Entry{Kind: "omitted", FromLine: fromLine, ToLine: toLine, OmittedLines: omitted})
			fromLine += omitted
			toLine += omitted
			for _, value := range ops[end-ContextLines : end] {
				entries = append(entries, lineEntry(value, &fromLine, &toLine))
			}
		} else {
			for _, value := range ops[start:end] {
				entries = append(entries, lineEntry(value, &fromLine, &toLine))
			}
		}
		start = end
	}
	return entries
}

func lineEntry(value op, fromLine, toLine *uint64) Entry {
	entry := Entry{Kind: value.kind, Text: value.text}
	switch value.kind {
	case "unchanged":
		entry.FromLine, entry.ToLine = *fromLine, *toLine
		*fromLine++
		*toLine++
	case "removed":
		entry.FromLine = *fromLine
		*fromLine++
	case "added":
		entry.ToLine = *toLine
		*toLine++
	}
	return entry
}

func hunks(entries []Entry) []Hunk {
	if len(entries) == 0 {
		return []Hunk{}
	}
	result := make([]Hunk, 0, (len(entries)+63)/64)
	for start := 0; start < len(entries); start += 64 {
		part := entries[start:min(start+64, len(entries))]
		from, to := uint64(0), uint64(0)
		for _, entry := range part {
			if from == 0 {
				from = entry.FromLine
			}
			if to == 0 {
				to = entry.ToLine
			}
		}
		result = append(result, Hunk{FromStart: from, ToStart: to, Entries: append([]Entry(nil), part...)})
	}
	return result
}

func encodeCursor(binding string, offset int) string {
	digest := sha256.Sum256([]byte(binding))
	value := "v1." + hex.EncodeToString(digest[:8]) + "." + strconv.Itoa(offset)
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeCursor(cursor, binding string, total int) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, errors.New("invalid diff cursor")
	}
	parts := strings.Split(string(decoded), ".")
	want := encodeCursor(binding, 0)
	wantDecoded, _ := base64.RawURLEncoding.DecodeString(want)
	wantParts := strings.Split(string(wantDecoded), ".")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != wantParts[1] {
		return 0, errors.New("invalid diff cursor")
	}
	offset, err := strconv.Atoi(parts[2])
	if err != nil || offset < 0 || offset >= total {
		return 0, errors.New("invalid diff cursor")
	}
	return offset, nil
}
