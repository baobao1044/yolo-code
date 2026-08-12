// Provider presets (kiểu AI SDK / Models.dev): một registry các LLM provider
// OpenAI-compatible, mỗi preset carry base-url + default model + key env var.
// User chọn provider qua `YOLO_PROVIDER` env hoặc `/provider <name>` slash
// command trong TUI. ResolveProvider đọc env → lookup preset → build provider;
// fallback về OpenAICompatProviderFromEnv (env-only) → stub.
//
// stdlib-only (cognitive imports only event + stdlib + prompt).

package cognitive

import (
	"os"
	"strconv"
	"strings"
)

// ProviderPreset là cấu hình preset cho một LLM provider. Name là key lookup
// (case-insensitive); BaseURL là OpenAI-compatible /chat/completions endpoint;
// DefaultModel là model mặc định; NeedsKey=false cho local server (Ollama, LM
// Studio); KeyEnv là env var đọc API key (fallback YOLO_API_KEY); Window là
// context window size (string, parse thành int); Description là mô tả ngắn.
type ProviderPreset struct {
	Name        string
	BaseURL     string
	DefaultModel string
	NeedsKey    bool
	KeyEnv      string
	Window      string
	Description string
}

// providerPresets là registry cố định ~28 provider phổ biến (kiểu AI SDK 75+,
// nhưng chỉ list những provider OpenAI-compatible + stable). Sort theo name
// để ListProviders trả deterministic (S5).
var providerPresets = []ProviderPreset{
	{Name: "openai", BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4o", NeedsKey: true, KeyEnv: "OPENAI_API_KEY", Window: "128000", Description: "OpenAI GPT models"},
	{Name: "anthropic", BaseURL: "https://api.anthropic.com/v1", DefaultModel: "claude-3-5-sonnet-20241022", NeedsKey: true, KeyEnv: "ANTHROPIC_API_KEY", Window: "200000", Description: "Anthropic Claude (OpenAI-compat mode)"},
	{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", DefaultModel: "openai/gpt-4o", NeedsKey: true, KeyEnv: "OPENROUTER_API_KEY", Window: "128000", Description: "OpenRouter — 300+ models, one API"},
	{Name: "together", BaseURL: "https://api.together.xyz/v1", DefaultModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo", NeedsKey: true, KeyEnv: "TOGETHER_API_KEY", Window: "128000", Description: "Together AI — open models"},
	{Name: "groq", BaseURL: "https://api.groq.com/openai/v1", DefaultModel: "llama-3.3-70b-versatile", NeedsKey: true, KeyEnv: "GROQ_API_KEY", Window: "128000", Description: "Groq — ultra-fast inference"},
	{Name: "mistral", BaseURL: "https://api.mistral.ai/v1", DefaultModel: "mistral-large-latest", NeedsKey: true, KeyEnv: "MISTRAL_API_KEY", Window: "128000", Description: "Mistral AI"},
	{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat", NeedsKey: true, KeyEnv: "DEEPSEEK_API_KEY", Window: "64000", Description: "DeepSeek"},
	{Name: "fireworks", BaseURL: "https://api.fireworks.ai/inference/v1", DefaultModel: "accounts/fireworks/models/llama-v3p3-70b-instruct", NeedsKey: true, KeyEnv: "FIREWORKS_API_KEY", Window: "128000", Description: "Fireworks AI"},
	{Name: "perplexity", BaseURL: "https://api.perplexity.ai", DefaultModel: "llama-3.1-sonar-large-128k-online", NeedsKey: true, KeyEnv: "PERPLEXITY_API_KEY", Window: "128000", Description: "Perplexity — online models"},
	{Name: "reka", BaseURL: "https://api.reka.ai/v1", DefaultModel: "reka-core", NeedsKey: true, KeyEnv: "REKA_API_KEY", Window: "128000", Description: "Reka AI"},
	{Name: "ai21", BaseURL: "https://api.ai21.com/studio/v1", DefaultModel: "jamba-1.5-large", NeedsKey: true, KeyEnv: "AI21_API_KEY", Window: "256000", Description: "AI21 Labs Jamba"},
	{Name: "cohere", BaseURL: "https://api.cohere.com/v1", DefaultModel: "command-r-plus-08-2024", NeedsKey: true, KeyEnv: "COHERE_API_KEY", Window: "128000", Description: "Cohere Command R+"},
	{Name: "nvidia", BaseURL: "https://integrate.api.nvidia.com/v1", DefaultModel: "meta/llama-3.3-70b-instruct", NeedsKey: true, KeyEnv: "NVIDIA_API_KEY", Window: "128000", Description: "NVIDIA NIM"},
	{Name: "cloudflare", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1", DefaultModel: "@cf/meta/llama-3.3-70b-instruct", NeedsKey: true, KeyEnv: "CLOUDFLARE_API_KEY", Window: "128000", Description: "Cloudflare Workers AI"},
	{Name: "aimlapi", BaseURL: "https://api.aimlapi.com/v1", DefaultModel: "gpt-4o", NeedsKey: true, KeyEnv: "AIMLAPI_API_KEY", Window: "128000", Description: "AI/ML API — multi-provider"},
	{Name: "huggingface", BaseURL: "https://api-inference.huggingface.co/v1", DefaultModel: "meta-llama/Llama-3.3-70B-Instruct", NeedsKey: true, KeyEnv: "HF_API_KEY", Window: "64000", Description: "HuggingFace Inference API"},
	{Name: "siliconflow", BaseURL: "https://api.siliconflow.cn/v1", DefaultModel: "deepseek-ai/DeepSeek-V3", NeedsKey: true, KeyEnv: "SILICONFLOW_API_KEY", Window: "64000", Description: "SiliconFlow"},
	{Name: "novita", BaseURL: "https://api.novita.ai/v3/openai", DefaultModel: "deepseek/deepseek-v3-turbo", NeedsKey: true, KeyEnv: "NOVITA_API_KEY", Window: "64000", Description: "NovitaAI"},
	{Name: "sambanova", BaseURL: "https://api.sambanova.ai/v1", DefaultModel: "Meta-Llama-3.3-70B-Instruct", NeedsKey: true, KeyEnv: "SAMBANOVA_API_KEY", Window: "128000", Description: "SambaNova"},
	{Name: "lepton", BaseURL: "https://api.lepton.ai/v1", DefaultModel: "llama3-3-70b", NeedsKey: true, KeyEnv: "LEPTON_API_KEY", Window: "128000", Description: "Lepton AI"},
	{Name: "volcano", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", DefaultModel: "doubao-pro-32k", NeedsKey: true, KeyEnv: "VOLC_API_KEY", Window: "128000", Description: "Volcano Engine (Doubao)"},
	{Name: "qwen", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", DefaultModel: "qwen-plus", NeedsKey: true, KeyEnv: "DASHSCOPE_API_KEY", Window: "128000", Description: "Qwen (Alibaba DashScope)"},
	{Name: "zhipu", BaseURL: "https://open.bigmodel.cn/api/paas/v4", DefaultModel: "glm-4-plus", NeedsKey: true, KeyEnv: "ZHIPU_API_KEY", Window: "128000", Description: "ZhipuAI GLM"},
	{Name: "yi", BaseURL: "https://api.lingyiwanwu.com/v1", DefaultModel: "yi-large", NeedsKey: true, KeyEnv: "YI_API_KEY", Window: "128000", Description: "01.AI Yi"},
	{Name: "moonshot", BaseURL: "https://api.moonshot.cn/v1", DefaultModel: "moonshot-v1-128k", NeedsKey: true, KeyEnv: "MOONSHOT_API_KEY", Window: "128000", Description: "Moonshot Kimi"},
	{Name: "minimax", BaseURL: "https://api.minimax.chat/v1", DefaultModel: "abab6.5s-chat", NeedsKey: true, KeyEnv: "MINIMAX_API_KEY", Window: "128000", Description: "MiniMax"},
	{Name: "wandb", BaseURL: "https://api.inference.wandb.ai/v1", DefaultModel: "meta-llama/Llama-3.1-8B-Instruct", NeedsKey: true, KeyEnv: "WANDB_API_KEY", Window: "128000", Description: "W&B Serverless Inference (OpenAI-compat)"},
	{Name: "ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3.3", NeedsKey: false, Window: "32000", Description: "Ollama — local models (no API key)"},
	{Name: "lmstudio", BaseURL: "http://localhost:1234/v1", DefaultModel: "local-model", NeedsKey: false, Window: "32000", Description: "LM Studio — local models (no API key)"},
}

// LookupProvider tìm preset theo name (case-insensitive). Trả (preset, true)
// nếu tìm thấy; (zero, false) nếu không. Dùng trong ResolveProvider + /provider
// slash command.
func LookupProvider(name string) (ProviderPreset, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range providerPresets {
		if strings.ToLower(p.Name) == n {
			return p, true
		}
	}
	return ProviderPreset{}, false
}

// ListProviders trả copy của registry (để caller không mutate). Sort theo Name
// (đã sort trong khai báo). TUI /provider (no arg) hiển thị list này qua event
// response (TUI không import cognitive → driver gửi text).
func ListProviders() []ProviderPreset {
	out := make([]ProviderPreset, len(providerPresets))
	copy(out, providerPresets)
	return out
}

// ProviderFromPreset build một OpenAICompatProvider từ preset + API key. Parse
// Window string → int; empty/invalid → fallback 128000. Empty key cho local
// provider (NeedsKey=false) → provider vẫn build (Ollama/LM Studio ignore key).
func ProviderFromPreset(p ProviderPreset, apiKey string) *OpenAICompatProvider {
	window := 128_000
	if w, err := strconv.Atoi(p.Window); err == nil && w > 0 {
		window = w
	}
	model := p.DefaultModel
	if m := os.Getenv("YOLO_MODEL"); m != "" {
		model = m // user override
	}
	return NewOpenAICompatProvider(p.BaseURL, apiKey, model, window)
}

// resolveAPIKey cho preset: đọc KeyEnv (vd GROQ_API_KEY), fallback YOLO_API_KEY,
// fallback OPENAI_API_KEY. NeedsKey=false → empty string (local server).
func resolveAPIKey(p ProviderPreset) string {
	if !p.NeedsKey {
		return ""
	}
	if p.KeyEnv != "" {
		if k := os.Getenv(p.KeyEnv); k != "" {
			return k
		}
	}
	if k := os.Getenv("YOLO_API_KEY"); k != "" {
		return k
	}
	if k := os.Getenv("OPENAI_API_KEY"); k != "" {
		return k
	}
	return ""
}

// ResolveProvider đọc YOLO_PROVIDER env → lookup preset → build provider từ
// preset + key. Nếu YOLO_PROVIDER không set hoặc không tìm thấy preset, fallback
// OpenAICompatProviderFromEnv (env-only path). Nếu vẫn nil, fallback stub.
// Đây là entry point duy nhất cho composition root (headless + tui_runner).
func ResolveProvider() Provider {
	if name := os.Getenv("YOLO_PROVIDER"); name != "" {
		if p, ok := LookupProvider(name); ok {
			key := resolveAPIKey(p)
			if !p.NeedsKey || key != "" {
				return ProviderFromPreset(p, key)
			}
			// Preset needs key but none found → fall through to env path
		}
	}
	if p := OpenAICompatProviderFromEnv(); p != nil {
		return p
	}
	return NewStubProvider(128_000)
}
