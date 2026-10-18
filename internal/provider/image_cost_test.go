package provider

import (
	"math"
	"testing"
)

func TestEstimateOpenAIImageCost(t *testing.T) {
	cases := []struct {
		name  string
		req   ImageRequest
		usage *ImageUsage
		want  float64
	}{
		{"dall-e-3 standard square", ImageRequest{Model: "dall-e-3"}, nil, 0.04},
		{"dall-e-3 standard wide", ImageRequest{Model: "dall-e-3", Size: "1792x1024"}, nil, 0.08},
		{"dall-e-3 hd square", ImageRequest{Model: "dall-e-3", Quality: "hd"}, nil, 0.08},
		{"dall-e-3 hd wide", ImageRequest{Model: "dall-e-3", Quality: "hd", Size: "1024x1792"}, nil, 0.12},
		{"dall-e-3 n=2", ImageRequest{Model: "dall-e-3", N: 2}, nil, 0.08},
		{"dall-e-2 256", ImageRequest{Model: "dall-e-2", Size: "256x256"}, nil, 0.016},
		{"dall-e-2 512", ImageRequest{Model: "dall-e-2", Size: "512x512"}, nil, 0.018},
		{"dall-e-2 1024", ImageRequest{Model: "dall-e-2", Size: "1024x1024", N: 3}, nil, 0.06},
		{"gpt-image-1 high wide no usage", ImageRequest{Model: "gpt-image-1", Quality: "high", Size: "1536x1024"}, nil, 0.25},
		{"gpt-image-1 low square no usage", ImageRequest{Model: "gpt-image-1", Quality: "low"}, nil, 0.011},
		{"gpt-image-1 usage", ImageRequest{Model: "gpt-image-1", Quality: "high"}, &ImageUsage{
			InputTokens: 1050, OutputTokens: 4160, TotalTokens: 5210,
			InputTokensDetails: &ImageInputTokensDetails{TextTokens: 50, ImageTokens: 1000},
		}, (50*5.0 + 1000*10.0 + 4160*40.0) / 1e6},
	}
	for _, c := range cases {
		if got := estimateOpenAIImageCost(c.req, c.usage); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: cost = %v, want %v", c.name, got, c.want)
		}
	}
}
