package codex

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/samber/lo"

	"github.com/looplj/axonhub/llm"
)

func normalizeCodexImageRequest(req *llm.Request) {
	if req == nil || req.Image == nil {
		return
	}

	img := req.Image
	prompt := strings.TrimSpace(img.Prompt)
	if prompt == "" {
		prompt = "Generate an image."
	}

	parts := []llm.MessageContentPart{{
		Type: "text",
		Text: lo.ToPtr(prompt),
	}}
	for _, image := range img.Images {
		if len(image) == 0 {
			continue
		}
		parts = append(parts, llm.MessageContentPart{
			Type: "image_url",
			ImageURL: &llm.ImageURL{
				URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(image),
			},
		})
	}

	req.RequestType = llm.RequestTypeChat
	req.Messages = []llm.Message{{
		Role: "user",
		Content: llm.MessageContent{
			MultipleContent: parts,
		},
	}}
	req.Tools = []llm.Tool{{
		Type: llm.ToolTypeImageGeneration,
		ImageGeneration: &llm.ImageGeneration{
			Model:             req.Model,
			Background:        img.Background,
			InputFidelity:     img.InputFidelity,
			Moderation:        img.Moderation,
			OutputCompression: img.OutputCompression,
			OutputFormat:      img.OutputFormat,
			PartialImages:     img.PartialImages,
			N:                 img.N,
			ResponseFormat:    img.ResponseFormat,
			Quality:           img.Quality,
			Size:              img.Size,
			Style:             img.Style,
		},
	}}
	req.Stream = lo.ToPtr(true)
	if req.ParallelToolCalls == nil {
		req.ParallelToolCalls = lo.ToPtr(true)
	}
	if req.TransformerMetadata == nil {
		req.TransformerMetadata = map[string]any{}
	}
	req.TransformerMetadata["codex_image_request"] = true
}

func injectCodexImageToolChoice(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	payload["tool_choice"] = map[string]any{"type": llm.ToolTypeImageGeneration}
	if _, ok := payload["parallel_tool_calls"]; !ok {
		payload["parallel_tool_calls"] = true
	}

	nextBody, err := json.Marshal(payload)
	if err != nil {
		return body
	}

	return nextBody
}

func convertCodexResponseToImage(resp *llm.Response) {
	if resp == nil {
		return
	}

	imageResp := &llm.ImageResponse{
		Created: resp.Created,
		Data:    make([]llm.ImageData, 0),
	}

	for _, choice := range resp.Choices {
		msg := choice.Message
		if msg == nil {
			continue
		}
		for _, part := range msg.Content.MultipleContent {
			if part.Type != "image_url" || part.ImageURL == nil {
				continue
			}
			if part.TransformerMetadata != nil {
				setImageResponseMetadata(imageResp, part.TransformerMetadata)
			}
			imageResp.Data = append(imageResp.Data, imageDataFromURL(part.ImageURL.URL))
		}
	}

	if len(imageResp.Data) == 0 {
		return
	}

	resp.Image = imageResp
}

func imageDataFromURL(url string) llm.ImageData {
	const base64Marker = ";base64,"
	if comma := strings.LastIndex(url, base64Marker); comma >= 0 {
		return llm.ImageData{B64JSON: url[comma+len(base64Marker):]}
	}

	return llm.ImageData{URL: url}
}

func setImageResponseMetadata(resp *llm.ImageResponse, metadata map[string]any) {
	if value, ok := metadata["background"].(string); ok {
		resp.Background = value
	}
	if value, ok := metadata["output_format"].(string); ok {
		resp.OutputFormat = value
	}
	if value, ok := metadata["quality"].(string); ok {
		resp.Quality = value
	}
	if value, ok := metadata["size"].(string); ok {
		resp.Size = value
	}
}
