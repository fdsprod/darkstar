package commonimage

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"reflect"
	"testing"

	"darkstar/src/ports/contentprocessor"
)

func TestCommonImagesProduceModelImageAndBoundedPreview(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, mediaType string
		encode          func(io.Writer, image.Image) error
	}{
		{name: "png", mediaType: "image/png", encode: png.Encode},
		{name: "jpeg", mediaType: "image/jpeg", encode: func(writer io.Writer, value image.Image) error { return jpeg.Encode(writer, value, nil) }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var source bytes.Buffer
			if err := test.encode(&source, testImage(800, 400)); err != nil {
				t.Fatal(err)
			}
			sink := &captureSink{}
			result, err := New().Process(context.Background(), request(test.mediaType, source.Bytes()), sink)
			if err != nil {
				t.Fatal(err)
			}
			wantKinds := []contentprocessor.RepresentationKind{contentprocessor.RepresentationImage, contentprocessor.RepresentationPreview}
			if !reflect.DeepEqual(sink.kinds, wantKinds) || len(result.Representations) != 2 {
				t.Fatalf("kinds/result = %#v / %#v", sink.kinds, result)
			}
			if sink.metadata[0]["width"] != "800" || sink.metadata[0]["height"] != "400" || sink.metadata[0]["modelUsable"] != "true" {
				t.Fatalf("image metadata = %#v", sink.metadata[0])
			}
			configuration, err := png.DecodeConfig(bytes.NewReader(sink.contents[1]))
			if err != nil || configuration.Width != 512 || configuration.Height != 256 {
				t.Fatalf("preview config = %#v, %v", configuration, err)
			}
			if sink.metadata[1]["sourceWidth"] != "800" || sink.metadata[1]["resizeAlgorithm"] != "nearest_neighbor" {
				t.Fatalf("preview metadata = %#v", sink.metadata[1])
			}
		})
	}
}

func TestCommonImagesDegradeSafely(t *testing.T) {
	t.Parallel()
	processor := New()
	malformed := request("image/png", []byte("not an image"))
	result, err := processor.Process(context.Background(), malformed, &captureSink{})
	if err != nil || len(result.Diagnostics) == 0 {
		t.Fatalf("malformed result = %#v, %v", result, err)
	}

	var source bytes.Buffer
	if err := png.Encode(&source, testImage(20, 20)); err != nil {
		t.Fatal(err)
	}
	limited := request("image/png", source.Bytes())
	limited.Limits.Pixels = 399
	result, err = processor.Process(context.Background(), limited, &captureSink{})
	if err != nil || !result.Limited || len(result.Diagnostics) == 0 {
		t.Fatalf("pixel-limited result = %#v, %v", result, err)
	}
}

func TestWebPProducesModelImageAndPreview(t *testing.T) {
	t.Parallel()
	content, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	result, err := New().Process(context.Background(), request("image/webp", content), sink)
	if err != nil || len(result.Representations) != 2 || sink.metadata[0]["format"] != "webp" {
		t.Fatalf("WebP result = %#v, sink = %#v, error = %v", result, sink, err)
	}
}

func TestConfiguredRegisteredImageFormat(t *testing.T) {
	t.Parallel()
	processor := NewWithConfig(Config{AdditionalMediaTypes: []string{"image/gif"}, PreviewMaxDimension: 64})
	support, err := processor.Supports(context.Background(), contentprocessor.SourceDescriptor{DetectedMediaType: "image/gif"})
	if err != nil || support.State != contentprocessor.SupportSupported {
		t.Fatalf("configured support = %#v, %v", support, err)
	}
}

func request(mediaType string, content []byte) contentprocessor.ProcessRequest {
	return contentprocessor.ProcessRequest{
		OperationID: "operation-image", IdempotencyKey: "derive-image",
		Source:  contentprocessor.SourceDescriptor{ArtifactID: "artifact_image", DeclaredMediaType: mediaType, DetectedMediaType: mediaType},
		Content: bytes.NewReader(content), PolicyVersion: "artifact-context/v1alpha1",
		Limits: contentprocessor.Limits{SourceBytes: 4 << 20, OutputBytes: 2 << 20, Pixels: 40_000_000, Representations: 8},
	}
}

func testImage(width, height int) image.Image {
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 0x7f, A: 0xff})
		}
	}
	return value
}

type captureSink struct {
	kinds    []contentprocessor.RepresentationKind
	contents [][]byte
	metadata []map[string]string
}

func (sink *captureSink) Store(_ context.Context, representation contentprocessor.Representation) (contentprocessor.Receipt, error) {
	content, err := io.ReadAll(representation.Content)
	if err != nil {
		return contentprocessor.Receipt{}, err
	}
	sink.kinds = append(sink.kinds, representation.Kind)
	sink.contents = append(sink.contents, content)
	sink.metadata = append(sink.metadata, representation.Metadata)
	return contentprocessor.Receipt{RepresentationID: "representation-image", Digest: representation.Digest, Size: representation.Size}, nil
}
