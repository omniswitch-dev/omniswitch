package provider

import (
	"context"
	"fmt"
	"strings"
)

type AliasProvider struct {
	name  string
	inner Provider
}

func NewAlias(name string, inner Provider) *AliasProvider {
	return &AliasProvider{name: NormalizeAlias(name), inner: inner}
}

func NormalizeAlias(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if strings.HasPrefix(name, "@") {
		return name
	}
	return "@" + name
}

func (p *AliasProvider) Name() string {
	return p.name
}

func (p *AliasProvider) Models() []ModelInfo {
	innerModels := p.inner.Models()
	out := make([]ModelInfo, 0, len(innerModels))
	for _, model := range innerModels {
		model.ID = p.modelID(model.ID)
		model.Provider = p.name
		model.OwnedBy = p.name
		out = append(out, model)
	}
	return out
}

func (p *AliasProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	originalModel := req.Model
	req.Model = p.stripModel(req.Model)
	resp, meta, err := p.inner.ChatCompletion(ctx, req)
	meta.ProviderType = p.inner.Name()
	meta.Provider = p.name
	meta.Model = originalModel
	if resp.Model != "" {
		resp.Model = originalModel
	}
	return resp, meta, err
}

func (p *AliasProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	originalModel := req.Model
	req.Model = p.stripModel(req.Model)
	if streamer, ok := p.inner.(StreamProvider); ok {
		chunks, meta, err := streamer.ChatCompletionStream(ctx, req)
		meta.ProviderType = p.inner.Name()
		meta.Provider = p.name
		meta.Model = originalModel
		return p.rewriteStreamModel(chunks, originalModel), meta, err
	}
	resp, meta, err := p.inner.ChatCompletion(ctx, req)
	if err != nil {
		return nil, meta, err
	}
	meta.ProviderType = p.inner.Name()
	meta.Provider = p.name
	meta.Model = originalModel
	if resp.Model != "" {
		resp.Model = originalModel
	}
	return StreamFromResponse(ctx, resp), meta, nil
}

func (p *AliasProvider) Embeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, ProviderMeta, error) {
	provider, ok := p.inner.(EmbeddingProvider)
	if !ok {
		return EmbeddingResponse{}, ProviderMeta{Provider: p.name, Model: req.Model}, fmt.Errorf("provider %q does not support embeddings", p.inner.Name())
	}
	originalModel := req.Model
	req.Model = p.stripModel(req.Model)
	response, meta, err := provider.Embeddings(ctx, req)
	meta.ProviderType = p.inner.Name()
	meta.Provider = p.name
	meta.Model = originalModel
	if response.Model != "" {
		response.Model = originalModel
	}
	return response, meta, err
}

func (p *AliasProvider) stripModel(model string) string {
	prefix := p.name + "/"
	if strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}

func (p *AliasProvider) modelID(model string) string {
	return p.name + "/" + strings.TrimPrefix(model, "/")
}

func (p *AliasProvider) rewriteStreamModel(chunks <-chan ChatResponseChunk, model string) <-chan ChatResponseChunk {
	if chunks == nil {
		return nil
	}
	out := make(chan ChatResponseChunk)
	go func() {
		defer close(out)
		for chunk := range chunks {
			if chunk.Model != "" {
				chunk.Model = model
			}
			out <- chunk
		}
	}()
	return out
}
