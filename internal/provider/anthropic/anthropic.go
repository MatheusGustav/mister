// Package anthropic implements provider.Provider on top of the Anthropic API.
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"

	"github.com/MatheusGustav/mister/internal/provider"
)

const (
	Name      = "anthropic"
	APIKeyEnv = "ANTHROPIC_API_KEY"
)

// ErrNoAPIKey is returned when the API key is not in the environment.
var ErrNoAPIKey = errors.New("falta a chave da API: defina a variável de ambiente " + APIKeyEnv)

type Client struct {
	api sdk.Client
}

// New builds a client that reads the key from ANTHROPIC_API_KEY.
func New() (*Client, error) {
	if os.Getenv(APIKeyEnv) == "" {
		return nil, ErrNoAPIKey
	}
	return NewWithOptions(), nil
}

// NewWithOptions builds a client with explicit SDK options.
func NewWithOptions(opts ...option.RequestOption) *Client {
	return &Client{api: sdk.NewClient(opts...)}
}

func (c *Client) Name() string { return Name }

func (c *Client) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeWebSearch: true}
}

func (c *Client) Complete(ctx context.Context, req provider.Request) (*provider.Response, error) {
	params, err := buildParams(req)
	if err != nil {
		return nil, err
	}

	stream := c.api.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var msg sdk.BetaMessage
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return nil, fmt.Errorf("montando a resposta da anthropic: %w", err)
		}
		if req.OnText == nil {
			continue
		}
		if delta, ok := event.AsAny().(sdk.BetaRawContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(sdk.BetaTextDelta); ok {
				req.OnText(text.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, describeError(err)
	}
	return toResponse(msg)
}

func buildParams(req provider.Request) (sdk.BetaMessageNewParams, error) {
	params := sdk.BetaMessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		// The system block closes the fixed prefix (tools render before it); the
		// top-level marker follows the growing tail of the conversation.
		CacheControl: sdk.NewBetaCacheControlEphemeralParam(),
	}
	if req.System != "" {
		params.System = []sdk.BetaTextBlockParam{{
			Text:         req.System,
			CacheControl: sdk.NewBetaCacheControlEphemeralParam(),
		}}
	}
	if req.Effort != "" {
		params.OutputConfig = sdk.BetaOutputConfigParam{Effort: sdk.BetaOutputConfigEffort(req.Effort)}
	}
	if supportsFallback(req.Model) {
		params.Fallbacks = sdk.BetaFallbacksParamOfDefault()
		params.Betas = []sdk.AnthropicBeta{sdk.AnthropicBetaServerSideFallback2026_07_01}
	}

	for _, spec := range req.Tools {
		params.Tools = append(params.Tools, sdk.BetaToolUnionParam{OfTool: toolParam(spec)})
	}
	if req.WebSearch != nil {
		params.Tools = append(params.Tools, webSearchParam(req.Model, req.WebSearch.MaxUses))
	}

	for i, m := range req.Messages {
		converted, err := messageParam(m)
		if err != nil {
			return params, fmt.Errorf("mensagem %d do histórico: %w", i, err)
		}
		if converted != nil {
			params.Messages = append(params.Messages, *converted)
		}
	}
	return params, nil
}

// supportsFallback reports whether a refusal of this model can be re-served by
// another model on the server side.
func supportsFallback(model string) bool {
	return !strings.Contains(model, "haiku")
}

func toolParam(spec provider.ToolSpec) *sdk.BetaToolParam {
	schema := sdk.BetaToolInputSchemaParam{Properties: spec.Schema["properties"]}
	if required, ok := spec.Schema["required"].([]string); ok {
		schema.Required = required
	}
	return &sdk.BetaToolParam{
		Name:        spec.Name,
		Description: param.NewOpt(spec.Description),
		InputSchema: schema,
		// Large inputs (file contents) arrive as they are generated. The tool
		// registry validates every input before running it.
		EagerInputStreaming: param.NewOpt(true),
	}
}

func webSearchParam(model string, maxUses int) sdk.BetaToolUnionParam {
	uses := param.NewOpt(int64(maxUses))
	if strings.Contains(model, "haiku") {
		return sdk.BetaToolUnionParam{OfWebSearchTool20250305: &sdk.BetaWebSearchTool20250305Param{MaxUses: uses}}
	}
	return sdk.BetaToolUnionParam{OfWebSearchTool20260209: &sdk.BetaWebSearchTool20260209Param{MaxUses: uses}}
}

func messageParam(m provider.Message) (*sdk.BetaMessageParam, error) {
	blocks := make([]sdk.BetaContentBlockParamUnion, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		if len(b.Raw) > 0 && m.Provider == Name {
			var native sdk.BetaContentBlockParamUnion
			if err := json.Unmarshal(b.Raw, &native); err != nil {
				return nil, fmt.Errorf("bloco nativo ilegível: %w", err)
			}
			blocks = append(blocks, native)
			continue
		}
		switch b.Type {
		case provider.BlockText:
			if b.Text != "" {
				blocks = append(blocks, sdk.NewBetaTextBlock(b.Text))
			}
		case provider.BlockToolUse:
			blocks = append(blocks, sdk.NewBetaToolUseBlock(b.ToolUseID, json.RawMessage(b.Input), b.ToolName))
		case provider.BlockToolResult:
			blocks = append(blocks, toolResultParam(b))
		case provider.BlockOpaque:
			// Written by another provider: this one cannot read it.
		}
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	if m.Role == provider.RoleAssistant {
		return &sdk.BetaMessageParam{OfAssistant: &sdk.BetaAssistantMessageParam{Content: blocks}}, nil
	}
	user := sdk.NewBetaUserMessage(blocks...)
	return &user, nil
}

// toolResultParam sends the text of a tool result and then its pictures, each
// one after its label.
func toolResultParam(b provider.Block) sdk.BetaContentBlockParamUnion {
	result := sdk.NewBetaToolResultBlock(b.ToolUseID, b.Text, b.IsError)
	for _, img := range b.Images {
		if img.Label != "" {
			result.OfToolResult.Content = append(result.OfToolResult.Content,
				sdk.BetaToolResultBlockParamContentUnion{OfText: &sdk.BetaTextBlockParam{Text: img.Label}})
		}
		result.OfToolResult.Content = append(result.OfToolResult.Content, sdk.BetaToolResultBlockParamContentUnion{
			OfImage: &sdk.BetaImageBlockParam{Source: sdk.BetaImageBlockParamSourceUnion{
				OfBase64: &sdk.BetaBase64ImageSourceParam{
					Data:      base64.StdEncoding.EncodeToString(img.Data),
					MediaType: sdk.BetaBase64ImageSourceMediaType(img.MediaType),
				},
			}},
		})
	}
	return result
}

func toResponse(msg sdk.BetaMessage) (*provider.Response, error) {
	resp := &provider.Response{
		Stop: stopReason(msg.StopReason),
		Usage: provider.Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
		Message: provider.Message{Role: provider.RoleAssistant, Provider: Name},
	}
	if resp.Stop == provider.StopRefusal {
		// A refusal can arrive after partial output; that output is not an answer.
		resp.StopDetail = strings.TrimSpace(string(msg.StopDetails.Category) + " " + msg.StopDetails.Explanation)
		return resp, nil
	}

	for _, block := range msg.Content {
		raw, err := json.Marshal(block.ToParam())
		if err != nil {
			return nil, fmt.Errorf("serializando bloco da resposta: %w", err)
		}
		switch v := block.AsAny().(type) {
		case sdk.BetaTextBlock:
			resp.Message.Blocks = append(resp.Message.Blocks, provider.Block{Type: provider.BlockText, Text: v.Text, Raw: raw})
		case sdk.BetaToolUseBlock:
			input := json.RawMessage(v.JSON.Input.Raw())
			if !json.Valid(input) {
				// Input cut mid-generation: the registry rejects the empty object
				// and the model gets a clear error to retry.
				input, raw = json.RawMessage("{}"), nil
			}
			resp.Message.Blocks = append(resp.Message.Blocks, provider.Block{
				Type:      provider.BlockToolUse,
				ToolUseID: v.ID,
				ToolName:  v.Name,
				Input:     input,
				Raw:       raw,
			})
		default:
			resp.Message.Blocks = append(resp.Message.Blocks, provider.Block{Type: provider.BlockOpaque, Raw: raw})
		}
	}
	return resp, nil
}

func stopReason(r sdk.BetaStopReason) provider.StopReason {
	switch r {
	case sdk.BetaStopReasonEndTurn, sdk.BetaStopReasonStopSequence:
		return provider.StopEndTurn
	case sdk.BetaStopReasonToolUse:
		return provider.StopToolUse
	case sdk.BetaStopReasonMaxTokens:
		return provider.StopMaxTokens
	case sdk.BetaStopReasonPauseTurn:
		return provider.StopPause
	case sdk.BetaStopReasonRefusal:
		return provider.StopRefusal
	default:
		return provider.StopOther
	}
}

func describeError(err error) error {
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("chamando a anthropic: %w", err)
	}
	switch apiErr.StatusCode {
	case 401, 403:
		return fmt.Errorf("a anthropic recusou a chave (HTTP %d): confira %s: %w", apiErr.StatusCode, APIKeyEnv, err)
	case 429:
		return fmt.Errorf("limite de uso da anthropic atingido (HTTP 429): %w", err)
	default:
		return fmt.Errorf("a anthropic devolveu HTTP %d: %w", apiErr.StatusCode, err)
	}
}
