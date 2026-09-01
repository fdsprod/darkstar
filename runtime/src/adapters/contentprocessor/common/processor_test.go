package common

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
)

func TestCommonFormatsProduceDeterministicRepresentations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mediaType, input string
		kinds                  []contentprocessor.RepresentationKind
		first                  string
	}{
		{"text", "text/plain", "hello\r\nworld\r", []contentprocessor.RepresentationKind{contentprocessor.RepresentationText}, "hello\nworld\n"},
		{"markdown", "text/markdown", "# Evidence\r\n", []contentprocessor.RepresentationKind{contentprocessor.RepresentationText}, "# Evidence\n"},
		{"json", "application/json", `{"z":1,"a":2}`, []contentprocessor.RepresentationKind{contentprocessor.RepresentationStructured, contentprocessor.RepresentationPreview}, `{"a":2,"z":1}`},
		{"yaml", "application/yaml", "z: 1\na: 2\n", []contentprocessor.RepresentationKind{contentprocessor.RepresentationStructured, contentprocessor.RepresentationPreview}, `{"a":2,"z":1}`},
		{"csv", "text/csv", "name,value\r\na,1\r\n", []contentprocessor.RepresentationKind{contentprocessor.RepresentationTable, contentprocessor.RepresentationPreview}, `[["name","value"],["a","1"]]`},
		{"pdf", "application/pdf", "%PDF-1.4\n1 0 obj << /Type /Page >> stream\nBT (hello PDF) Tj ET\nendstream\n%%EOF", []contentprocessor.RepresentationKind{contentprocessor.RepresentationText}, "hello PDF\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sink := &captureSink{}
			result, err := New().Process(context.Background(), processRequest(test.mediaType, test.input), sink)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sink.kinds, test.kinds) || len(result.Representations) != len(test.kinds) {
				t.Fatalf("kinds/result = %#v / %#v", sink.kinds, result)
			}
			if got := string(sink.contents[0]); got != test.first {
				t.Fatalf("first representation = %q, want %q", got, test.first)
			}
		})
	}
}

func TestMalformedFormatsFailInIsolation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ mediaType, input string }{
		{"application/json", "{"},
		{"application/yaml", "a: &anchor 1\nb: *anchor\n"},
		{"text/csv", "a,b\n1\n"},
		{"application/pdf", "%PDF-1.4\nno text operators"},
	} {
		sink := &captureSink{}
		result, err := New().Process(context.Background(), processRequest(test.mediaType, test.input), sink)
		if err != nil || len(result.Diagnostics) == 0 || len(sink.contents) != 0 {
			t.Fatalf("Process(%s) = %#v, sink %#v, error %v", test.mediaType, result, sink, err)
		}
	}
}

func TestProcessorBoundsOutputAndReportsTruncation(t *testing.T) {
	t.Parallel()
	request := processRequest("text/plain", strings.Repeat("é", 10))
	request.Limits.OutputBytes = 5
	sink := &captureSink{}
	result, err := New().Process(context.Background(), request, sink)
	if err != nil || !result.Limited || string(sink.contents[0]) != "éé" || !sink.truncated[0] {
		t.Fatalf("bounded result = %#v, sink %#v, error %v", result, sink, err)
	}
}

func processRequest(mediaType, input string) contentprocessor.ProcessRequest {
	return contentprocessor.ProcessRequest{
		OperationID: "operation-test", IdempotencyKey: "derive-test",
		Source:  contentprocessor.SourceDescriptor{ArtifactID: "artifact_test", DeclaredMediaType: mediaType, DetectedMediaType: mediaType},
		Content: bytes.NewBufferString(input), PolicyVersion: "artifact-context/v1alpha1",
	}
}

type captureSink struct {
	kinds     []contentprocessor.RepresentationKind
	contents  [][]byte
	truncated []bool
}

func (sink *captureSink) Store(_ context.Context, representation contentprocessor.Representation) (contentprocessor.Receipt, error) {
	content, err := io.ReadAll(representation.Content)
	if err != nil {
		return contentprocessor.Receipt{}, err
	}
	sink.kinds = append(sink.kinds, representation.Kind)
	sink.contents = append(sink.contents, content)
	sink.truncated = append(sink.truncated, representation.Truncated)
	return contentprocessor.Receipt{RepresentationID: "representation-test", Digest: representation.Digest, Size: representation.Size}, nil
}
