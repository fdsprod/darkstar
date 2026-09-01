// Package common implements deterministic processors for the MVP text-bearing
// formats. Parse failures are returned as diagnostics so original artifacts stay
// durable and independently inspectable.
package common

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
	"go.yaml.in/yaml/v3"
)

const (
	Name    = "darkstar-common-formats"
	Version = "1.0.0"
)

var mediaTypes = []string{
	"application/json", "application/pdf", "application/yaml", "application/x-yaml",
	"text/csv", "text/markdown", "text/plain", "text/yaml",
}

type Processor struct{}

var _ contentprocessor.Processor = Processor{}

func New() Processor { return Processor{} }

func (Processor) Descriptor() contentprocessor.Descriptor {
	return contentprocessor.Descriptor{Name: Name, Version: Version, MediaTypes: append([]string(nil), mediaTypes...)}
}

func (Processor) Supports(_ context.Context, source contentprocessor.SourceDescriptor) (contentprocessor.Support, error) {
	mediaType := baseMediaType(source.DetectedMediaType)
	if !supported(mediaType) {
		mediaType = baseMediaType(source.DeclaredMediaType)
	}
	if supported(mediaType) {
		return contentprocessor.Support{State: contentprocessor.SupportSupported, MediaType: mediaType}, nil
	}
	return contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: mediaType,
		Diagnostics: []string{"no deterministic common-format processor is installed for this media type"}}, nil
}

func (processor Processor) Process(ctx context.Context, request contentprocessor.ProcessRequest, sink contentprocessor.Sink) (contentprocessor.ProcessResult, error) {
	if sink == nil || request.Content == nil || strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return contentprocessor.ProcessResult{}, errors.New("processor content, sink, operation ID, and idempotency key are required")
	}
	support, err := processor.Supports(ctx, request.Source)
	if err != nil {
		return contentprocessor.ProcessResult{}, err
	}
	if support.State != contentprocessor.SupportSupported {
		return contentprocessor.ProcessResult{Diagnostics: support.Diagnostics}, nil
	}
	content, limited, err := readBounded(request.Content, request.Limits.SourceBytes)
	if err != nil {
		return contentprocessor.ProcessResult{}, err
	}
	if limited {
		return contentprocessor.ProcessResult{Diagnostics: []string{"source exceeds processor byte limit"}, Limited: true}, nil
	}

	outputs, diagnostics := transform(support.MediaType, content)
	result := contentprocessor.ProcessResult{Diagnostics: diagnostics}
	for index, output := range outputs {
		if request.Limits.Representations > 0 && index >= request.Limits.Representations {
			result.Limited = true
			result.Diagnostics = append(result.Diagnostics, "representation count limit reached")
			break
		}
		bounded, truncated := truncateUTF8(output.content, request.Limits.OutputBytes)
		if truncated {
			result.Limited = true
		}
		representation := makeRepresentation(output.kind, output.mediaType, bounded, truncated, output.metadata)
		receipt, err := sink.Store(ctx, representation)
		if err != nil {
			return result, fmt.Errorf("store %s representation: %w", output.kind, err)
		}
		result.Representations = append(result.Representations, receipt)
	}
	return result, nil
}

type derived struct {
	kind      contentprocessor.RepresentationKind
	mediaType string
	content   []byte
	metadata  map[string]string
}

func transform(mediaType string, content []byte) ([]derived, []string) {
	switch mediaType {
	case "text/plain", "text/markdown":
		if !utf8.Valid(content) {
			return nil, []string{"text is not valid UTF-8"}
		}
		normalized := normalizeText(content)
		return []derived{{kind: contentprocessor.RepresentationText, mediaType: "text/plain; charset=utf-8", content: normalized}}, nil
	case "application/json":
		canonical, err := canonicalJSON(content)
		if err != nil {
			return nil, []string{"JSON parse failed: " + err.Error()}
		}
		return structuredOutputs(canonical, "json"), nil
	case "application/yaml", "application/x-yaml", "text/yaml":
		canonical, err := canonicalYAML(content)
		if err != nil {
			return nil, []string{"YAML parse failed: " + err.Error()}
		}
		return structuredOutputs(canonical, "yaml"), nil
	case "text/csv":
		table, preview, rows, columns, err := canonicalCSV(content)
		if err != nil {
			return nil, []string{"CSV parse failed: " + err.Error()}
		}
		metadata := map[string]string{"rows": strconv.Itoa(rows), "columns": strconv.Itoa(columns)}
		return []derived{
			{kind: contentprocessor.RepresentationTable, mediaType: "application/json", content: table, metadata: metadata},
			{kind: contentprocessor.RepresentationPreview, mediaType: "text/csv; charset=utf-8", content: preview, metadata: metadata},
		}, nil
	case "application/pdf":
		text, pages, err := extractPDFText(content)
		if err != nil {
			return nil, []string{"PDF extraction failed: " + err.Error()}
		}
		return []derived{{kind: contentprocessor.RepresentationText, mediaType: "text/plain; charset=utf-8", content: text,
			metadata: map[string]string{"pages": strconv.Itoa(pages)}}}, nil
	default:
		return nil, []string{"unsupported media type"}
	}
}

func structuredOutputs(canonical []byte, format string) []derived {
	preview := append(append([]byte(nil), canonical...), '\n')
	return []derived{
		{kind: contentprocessor.RepresentationStructured, mediaType: "application/json", content: canonical, metadata: map[string]string{"sourceFormat": format}},
		{kind: contentprocessor.RepresentationPreview, mediaType: "text/plain; charset=utf-8", content: preview, metadata: map[string]string{"sourceFormat": format}},
	}
}

func canonicalJSON(content []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are not supported")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func canonicalYAML(content []byte) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple YAML documents are not supported")
	}
	if len(document.Content) != 1 {
		return nil, errors.New("YAML document has no root value")
	}
	value, err := yamlValue(document.Content[0])
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func yamlValue(node *yaml.Node) (any, error) {
	if node.Alias != nil || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return nil, errors.New("YAML aliases and anchors are not supported")
	}
	switch node.Kind {
	case yaml.MappingNode:
		value := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, errors.New("YAML mapping keys must be strings")
			}
			if _, exists := value[key.Value]; exists {
				return nil, fmt.Errorf("duplicate YAML mapping key %q", key.Value)
			}
			entry, err := yamlValue(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			value[key.Value] = entry
		}
		return value, nil
	case yaml.SequenceNode:
		value := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			entry, err := yamlValue(child)
			if err != nil {
				return nil, err
			}
			value = append(value, entry)
		}
		return value, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str":
			return node.Value, nil
		case "!!null":
			return nil, nil
		case "!!bool":
			return strconv.ParseBool(node.Value)
		case "!!int":
			return strconv.ParseInt(node.Value, 0, 64)
		case "!!float":
			return strconv.ParseFloat(node.Value, 64)
		default:
			return nil, fmt.Errorf("YAML tag %q is not supported", node.Tag)
		}
	default:
		return nil, fmt.Errorf("YAML node kind %d is not supported", node.Kind)
	}
}

func canonicalCSV(content []byte) ([]byte, []byte, int, int, error) {
	if !utf8.Valid(content) {
		return nil, nil, 0, 0, errors.New("CSV is not valid UTF-8")
	}
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = 0
	reader.ReuseRecord = false
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, 0, 0, err
	}
	columns := 0
	if len(records) > 0 {
		columns = len(records[0])
	}
	table, err := json.Marshal(records)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var preview bytes.Buffer
	writer := csv.NewWriter(&preview)
	writer.UseCRLF = false
	if err := writer.WriteAll(records); err != nil {
		return nil, nil, 0, 0, err
	}
	return table, preview.Bytes(), len(records), columns, nil
}

func extractPDFText(content []byte) ([]byte, int, error) {
	if !bytes.HasPrefix(content, []byte("%PDF-")) {
		return nil, 0, errors.New("missing PDF header")
	}
	if bytes.Contains(content, []byte("/Encrypt")) {
		return nil, 0, errors.New("encrypted PDFs are not supported")
	}
	pages := bytes.Count(content, []byte("/Type /Page")) - bytes.Count(content, []byte("/Type /Pages"))
	if pages < 1 {
		pages = 1
	}
	texts := make([]string, 0)
	for index := 0; index < len(content); index++ {
		if content[index] != '(' {
			continue
		}
		literal, next, ok := pdfLiteral(content, index+1)
		if !ok {
			continue
		}
		operator := strings.TrimSpace(string(content[next:min(next+8, len(content))]))
		if strings.HasPrefix(operator, "Tj") || strings.Contains(operator, "] TJ") {
			texts = append(texts, literal)
		}
		index = next - 1
	}
	if len(texts) == 0 {
		return nil, pages, errors.New("PDF contains no supported text operators")
	}
	return []byte(strings.Join(texts, "\n") + "\n"), pages, nil
}

func pdfLiteral(content []byte, start int) (string, int, bool) {
	var output strings.Builder
	depth := 1
	escaped := false
	for index := start; index < len(content); index++ {
		value := content[index]
		if escaped {
			switch value {
			case 'n':
				output.WriteByte('\n')
			case 'r':
				output.WriteByte('\r')
			case 't':
				output.WriteByte('\t')
			default:
				output.WriteByte(value)
			}
			escaped = false
			continue
		}
		if value == '\\' {
			escaped = true
			continue
		}
		switch value {
		case '(':
			depth++
			output.WriteByte(value)
		case ')':
			depth--
			if depth == 0 {
				return output.String(), index + 1, true
			}
			output.WriteByte(value)
		default:
			output.WriteByte(value)
		}
	}
	return "", start, false
}

func readBounded(reader io.Reader, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		content, err := io.ReadAll(reader)
		return content, false, err
	}
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	return content, int64(len(content)) > limit, nil
}

func truncateUTF8(content []byte, limit int64) ([]byte, bool) {
	if limit <= 0 || int64(len(content)) <= limit {
		return content, false
	}
	content = append([]byte(nil), content[:limit]...)
	for len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
	}
	return content, true
}

func makeRepresentation(kind contentprocessor.RepresentationKind, mediaType string, content []byte, truncated bool, metadata map[string]string) contentprocessor.Representation {
	digestBytes := sha256.Sum256(content)
	return contentprocessor.Representation{
		Kind: kind, MediaType: mediaType, Content: bytes.NewReader(content), Digest: hex.EncodeToString(digestBytes[:]),
		Size: int64(len(content)), TokenEstimate: int64((len(content) + 3) / 4), Truncated: truncated, Metadata: metadata,
	}
}

func supported(mediaType string) bool {
	for _, candidate := range mediaTypes {
		if mediaType == candidate {
			return true
		}
	}
	return false
}

func baseMediaType(value string) string {
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = value[:index]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeText(content []byte) []byte {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(content, []byte("\r"), []byte("\n"))
}
