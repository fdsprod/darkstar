// Package commonimage implements bounded, deterministic representations for
// common raster image formats. Original bytes remain immutable evidence; the
// image representation provides a model-usable reference and the preview is a
// derived PNG thumbnail.
package commonimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"slices"
	"strconv"
	"strings"

	"darkstar/src/ports/contentprocessor"
	_ "golang.org/x/image/webp"
)

const (
	Name                = "darkstar-common-images"
	Version             = "1.0.0"
	DefaultPreviewLimit = 512
)

var defaultMediaTypes = []string{"image/jpeg", "image/png", "image/webp"}

// Config permits additional image media types whose decoders are registered
// with Go's image package. The default PNG, JPEG, and WebP formats are always
// enabled.
type Config struct {
	AdditionalMediaTypes []string
	PreviewMaxDimension  int
}

type Processor struct {
	mediaTypes          []string
	previewMaxDimension int
}

var _ contentprocessor.Processor = Processor{}

func New() Processor { return NewWithConfig(Config{}) }

func NewWithConfig(config Config) Processor {
	mediaTypes := append([]string(nil), defaultMediaTypes...)
	for _, mediaType := range config.AdditionalMediaTypes {
		mediaType = baseMediaType(mediaType)
		if strings.HasPrefix(mediaType, "image/") && !slices.Contains(mediaTypes, mediaType) {
			mediaTypes = append(mediaTypes, mediaType)
		}
	}
	slices.Sort(mediaTypes)
	if config.PreviewMaxDimension <= 0 {
		config.PreviewMaxDimension = DefaultPreviewLimit
	}
	return Processor{mediaTypes: mediaTypes, previewMaxDimension: config.PreviewMaxDimension}
}

func (processor Processor) Descriptor() contentprocessor.Descriptor {
	return contentprocessor.Descriptor{Name: Name, Version: Version, MediaTypes: append([]string(nil), processor.mediaTypes...)}
}

func (processor Processor) Supports(_ context.Context, source contentprocessor.SourceDescriptor) (contentprocessor.Support, error) {
	mediaType := baseMediaType(source.DetectedMediaType)
	if !slices.Contains(processor.mediaTypes, mediaType) {
		mediaType = baseMediaType(source.DeclaredMediaType)
	}
	if slices.Contains(processor.mediaTypes, mediaType) {
		return contentprocessor.Support{State: contentprocessor.SupportSupported, MediaType: mediaType}, nil
	}
	return contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: mediaType,
		Diagnostics: []string{"no configured common-image processor is installed for this media type"}}, nil
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
	if err := ctx.Err(); err != nil {
		return contentprocessor.ProcessResult{}, err
	}

	configuration, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return contentprocessor.ProcessResult{Diagnostics: []string{"image metadata decode failed: " + err.Error()}}, nil
	}
	pixels, ok := pixelCount(configuration.Width, configuration.Height)
	if !ok || (request.Limits.Pixels > 0 && pixels > request.Limits.Pixels) {
		return contentprocessor.ProcessResult{Diagnostics: []string{"image pixel limit exceeded"}, Limited: true}, nil
	}
	metadata := imageMetadata(configuration.Width, configuration.Height, format, support.MediaType)
	outputs := []contentprocessor.Representation{makeRepresentation(contentprocessor.RepresentationImage, support.MediaType, content, metadata)}
	diagnostics := make([]string, 0)

	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		diagnostics = append(diagnostics, "image preview decode failed: "+err.Error())
	} else {
		preview := thumbnail(decoded, processor.previewMaxDimension)
		var encoded bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		if err := encoder.Encode(&encoded, preview); err != nil {
			diagnostics = append(diagnostics, "image preview encode failed: "+err.Error())
		} else if request.Limits.OutputBytes > 0 && int64(encoded.Len()) > request.Limits.OutputBytes {
			diagnostics = append(diagnostics, "image preview exceeds output byte limit")
		} else {
			previewBounds := preview.Bounds()
			previewMetadata := imageMetadata(previewBounds.Dx(), previewBounds.Dy(), "png", "image/png")
			previewMetadata["sourceWidth"] = strconv.Itoa(configuration.Width)
			previewMetadata["sourceHeight"] = strconv.Itoa(configuration.Height)
			previewMetadata["sourceFormat"] = format
			previewMetadata["resizeAlgorithm"] = "nearest_neighbor"
			outputs = append(outputs, makeRepresentation(contentprocessor.RepresentationPreview, "image/png", encoded.Bytes(), previewMetadata))
		}
	}

	result := contentprocessor.ProcessResult{Diagnostics: diagnostics}
	for index, output := range outputs {
		if request.Limits.Representations > 0 && index >= request.Limits.Representations {
			result.Limited = true
			result.Diagnostics = append(result.Diagnostics, "representation count limit reached")
			break
		}
		receipt, err := sink.Store(ctx, output)
		if err != nil {
			return result, fmt.Errorf("store %s representation: %w", output.Kind, err)
		}
		result.Representations = append(result.Representations, receipt)
	}
	return result, nil
}

func imageMetadata(width, height int, format, mediaType string) map[string]string {
	return map[string]string{
		"width": widthString(width), "height": widthString(height), "format": strings.ToLower(format),
		"mediaType": mediaType, "modelUsable": "true",
	}
}

func widthString(value int) string { return strconv.Itoa(value) }

func pixelCount(width, height int) (int64, bool) {
	if width <= 0 || height <= 0 {
		return 0, false
	}
	pixels := int64(width) * int64(height)
	return pixels, pixels/int64(width) == int64(height)
}

func thumbnail(source image.Image, maxDimension int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxDimension && height <= maxDimension {
		return source
	}
	var targetWidth, targetHeight int
	if width >= height {
		targetWidth = maxDimension
		targetHeight = max(1, height*maxDimension/width)
	} else {
		targetHeight = maxDimension
		targetWidth = max(1, width*maxDimension/height)
	}
	target := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := range targetHeight {
		sourceY := bounds.Min.Y + y*height/targetHeight
		for x := range targetWidth {
			sourceX := bounds.Min.X + x*width/targetWidth
			target.Set(x, y, color.NRGBAModel.Convert(source.At(sourceX, sourceY)))
		}
	}
	return target
}

func makeRepresentation(kind contentprocessor.RepresentationKind, mediaType string, content []byte, metadata map[string]string) contentprocessor.Representation {
	digest := sha256.Sum256(content)
	return contentprocessor.Representation{
		Kind: kind, MediaType: mediaType, Content: bytes.NewReader(content), Digest: hex.EncodeToString(digest[:]),
		Size: int64(len(content)), Metadata: metadata,
	}
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

func baseMediaType(value string) string {
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = value[:index]
	}
	return strings.ToLower(strings.TrimSpace(value))
}
