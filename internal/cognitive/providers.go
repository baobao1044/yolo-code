// Provider presets (kiểu AI SDK / Models.dev): một registry các LLM provider
// OpenAI-compatible, mỗi preset carry base-url + default model + key env var.
// User chọn provider qua `YOLO_PROVIDER` env hoặc `/provider <name>` slash
// command trong TUI. ResolveProvider đọc env → lookup preset → build provider;
// fallback về OpenAICompatProviderFromEnv (env-only). Không có provider nào
// resolve được → lỗi actionable, KHÔNG phải stub: stub chỉ chạy khi opt-in
// (YOLO_STUB=1 / YOLO_PROVIDER=stub). Mỗi preset chỉ đọc key env var của chính
// nó — không cross-provider fallback (credential leak).
//
// stdlib-only (cognitive imports only event + stdlib + prompt).

package cognitive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProviderPreset là cấu hình preset cho một LLM provider. Name là key lookup
// (case-insensitive); BaseURL là OpenAI-compatible /chat/completions endpoint;
// DefaultModel là model mặc định; NeedsKey=false cho local server (Ollama, LM
// Studio); KeyEnv là env var *duy nhất* đọc API key cho preset này (không có
// cross-provider fallback — xem resolveAPIKey); Window là
// context window size (string, parse thành int); Description là mô tả ngắn.
type ProviderPreset struct {
	Name         string
	BaseURL      string
	DefaultModel string
	NeedsKey     bool
	KeyEnv       string
	Window       string
	Description  string
}

// providerPresets là registry cố định ~28 provider phổ biến (kiểu AI SDK 75+,
// nhưng chỉ list những provider OpenAI-compatible + stable). Sort theo name
// để ListProviders trả deterministic (S5).
var providerPresets = []ProviderPreset{
	{Name: "openai", BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4o", NeedsKey: true, KeyEnv: "OPENAI_API_KEY", Window: "128000", Description: "OpenAI GPT models"},
	// claude-3-5-sonnet-20241022 đã retired (2025-10-28) — model retired trả 404,
	// nên `/provider anthropic` không kèm model là fail chắc chắn ở request đầu.
	// Claude 5 family có context 1M, không phải 200K của bản 3.5.
	{Name: "anthropic", BaseURL: "https://api.anthropic.com/v1", DefaultModel: "claude-opus-5", NeedsKey: true, KeyEnv: "ANTHROPIC_API_KEY", Window: "1000000", Description: "Anthropic Claude (OpenAI-compat mode)"},
	{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", DefaultModel: "openai/gpt-4o", NeedsKey: true, KeyEnv: "OPENROUTER_API_KEY", Window: "128000", Description: "OpenRouter — 300+ models, one API"},
	{Name: "together", BaseURL: "https://api.together.xyz/v1", DefaultModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo", NeedsKey: true, KeyEnv: "TOGETHER_API_KEY", Window: "128000", Description: "Together AI — open models"},
	{Name: "groq", BaseURL: "https://api.groq.com/openai/v1", DefaultModel: "llama-3.3-70b-versatile", NeedsKey: true, KeyEnv: "GROQ_API_KEY", Window: "128000", Description: "Groq — ultra-fast inference"},
	{Name: "mistral", BaseURL: "https://api.mistral.ai/v1", DefaultModel: "mistral-large-latest", NeedsKey: true, KeyEnv: "MISTRAL_API_KEY", Window: "128000", Description: "Mistral AI"},
	{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat", NeedsKey: true, KeyEnv: "DEEPSEEK_API_KEY", Window: "64000", Description: "DeepSeek"},
	{Name: "fireworks", BaseURL: "https://api.fireworks.ai/inference/v1", DefaultModel: "accounts/fireworks/models/llama-v3p3-70b-instruct", NeedsKey: true, KeyEnv: "FIREWORKS_API_KEY", Window: "128000", Description: "Fireworks AI"},
	// llama-3.1-sonar-large-128k-online is not in Perplexity's lineup any more;
	// the Sonar models were renamed to sonar / sonar-pro / sonar-reasoning-pro /
	// sonar-deep-research. The host is alive (401 unauthenticated), so this preset
	// failed at the model name rather than the connection. Separately, Perplexity
	// has superseded the Sonar Chat Completions API and supports it only until
	// 2026-09-27 — this preset needs revisiting before then, not just retitling.
	{Name: "perplexity", BaseURL: "https://api.perplexity.ai", DefaultModel: "sonar-pro", NeedsKey: true, KeyEnv: "PERPLEXITY_API_KEY", Window: "128000", Description: "Perplexity — online models"},
	{Name: "reka", BaseURL: "https://api.reka.ai/v1", DefaultModel: "reka-core", NeedsKey: true, KeyEnv: "REKA_API_KEY", Window: "128000", Description: "Reka AI"},
	{Name: "ai21", BaseURL: "https://api.ai21.com/studio/v1", DefaultModel: "jamba-1.5-large", NeedsKey: true, KeyEnv: "AI21_API_KEY", Window: "256000", Description: "AI21 Labs Jamba"},
	// Wrong in both fields. api.cohere.com/v1 is Cohere's NATIVE API, whose chat
	// path is /v1/chat with Cohere's own request shape — not /v1/chat/completions
	// in OpenAI shape, which is what this client sends. The host answers 401 to an
	// unauthenticated probe, so the endpoint looked healthy while being incapable
	// of serving the request the registry exists to make. The OpenAI-compatible
	// endpoint is a different host and path entirely (also 401, i.e. alive).
	// command-r-plus-08-2024 is likewise two generations stale.
	{Name: "cohere", BaseURL: "https://api.cohere.ai/compatibility/v1", DefaultModel: "command-a-plus-05-2026", NeedsKey: true, KeyEnv: "COHERE_API_KEY", Window: "128000", Description: "Cohere Command (OpenAI-compat endpoint)"},
	{Name: "nvidia", BaseURL: "https://integrate.api.nvidia.com/v1", DefaultModel: "meta/llama-3.3-70b-instruct", NeedsKey: true, KeyEnv: "NVIDIA_API_KEY", Window: "128000", Description: "NVIDIA NIM"},
	{Name: "cloudflare", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1", DefaultModel: "@cf/meta/llama-3.3-70b-instruct", NeedsKey: true, KeyEnv: "CLOUDFLARE_API_KEY", Window: "128000", Description: "Cloudflare Workers AI"},
	{Name: "aimlapi", BaseURL: "https://api.aimlapi.com/v1", DefaultModel: "gpt-4o", NeedsKey: true, KeyEnv: "AIMLAPI_API_KEY", Window: "128000", Description: "AI/ML API — multi-provider"},
	// api-inference.huggingface.co does not resolve — HuggingFace moved
	// OpenAI-compatible inference to router.huggingface.co. The model was already
	// right (verified against the live GET /models on the new host), so this is a
	// one-field repair: the preset was unusable purely because of where it pointed.
	{Name: "huggingface", BaseURL: "https://router.huggingface.co/v1", DefaultModel: "meta-llama/Llama-3.3-70B-Instruct", NeedsKey: true, KeyEnv: "HF_API_KEY", Window: "64000", Description: "HuggingFace Inference Providers (OpenAI-compat)"},
	{Name: "siliconflow", BaseURL: "https://api.siliconflow.cn/v1", DefaultModel: "deepseek-ai/DeepSeek-V3", NeedsKey: true, KeyEnv: "SILICONFLOW_API_KEY", Window: "64000", Description: "SiliconFlow"},
	{Name: "novita", BaseURL: "https://api.novita.ai/v3/openai", DefaultModel: "deepseek/deepseek-v3-turbo", NeedsKey: true, KeyEnv: "NOVITA_API_KEY", Window: "64000", Description: "NovitaAI"},
	{Name: "sambanova", BaseURL: "https://api.sambanova.ai/v1", DefaultModel: "Meta-Llama-3.3-70B-Instruct", NeedsKey: true, KeyEnv: "SAMBANOVA_API_KEY", Window: "128000", Description: "SambaNova"},
	// REMOVED — lepton. api.lepton.ai does not resolve. Lepton AI was absorbed
	// into NVIDIA DGX Cloud Lepton (lepton.ai now 301s to nvidia.com) and the API
	// host went with it. Unlike huggingface above there is no drop-in successor to
	// repoint at: what replaced it is an enterprise GPU platform, not an
	// OpenAI-compatible chat endpoint, so repointing would have been a guess.
	//
	// Deleted rather than left in place because an advertised preset that cannot
	// open a socket costs the user a key, a config change and a failed run to
	// discover. If it returns, this is one line — the original was:
	//   {Name: "lepton", BaseURL: "https://api.lepton.ai/v1", DefaultModel: "llama3-3-70b", NeedsKey: true, KeyEnv: "LEPTON_API_KEY", Window: "128000", Description: "Lepton AI"},
	// Whether the registry should carry a deprecation marker instead of dropping
	// entries outright is a spec decision nobody has made; today it has no way to
	// say "this existed and is gone", which is why this is a comment.
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

// resolveAPIKey cho preset: CHỈ đọc env var của chính provider đó (KeyEnv, vd
// GROQ_API_KEY). NeedsKey=false → empty string (local server).
//
// Không có cross-provider fallback. Trước đây hàm này fallback YOLO_API_KEY rồi
// OPENAI_API_KEY cho *mọi* preset: một user export sẵn OPENAI_API_KEY (rất phổ
// biến) mà chọn provider khác sẽ âm thầm gửi credential OpenAI của mình tới host
// của bên thứ ba trong Authorization header. OpenAI preset vẫn dùng được
// OPENAI_API_KEY vì đó chính là KeyEnv của nó. Mọi preset NeedsKey đều có KeyEnv
// riêng (pinned bởi TestEveryKeyedPresetHasKeyEnv), nên bỏ fallback không làm
// preset nào mất đường cấp key. YOLO_API_KEY vẫn dùng được cho env-only path
// (YOLO_BASE_URL + YOLO_API_KEY), nơi user tự khai endpoint.
func resolveAPIKey(p ProviderPreset) string {
	if !p.NeedsKey {
		return ""
	}
	if p.KeyEnv != "" {
		return os.Getenv(p.KeyEnv)
	}
	return ""
}

// unsubstitutedPlaceholder trả về token `{...}` đầu tiên còn sót lại trong một
// BaseURL (kèm dấu ngoặc, vd `{account_id}`), hoặc "" nếu URL đã dùng được ngay.
//
// Vài provider có base-url chứa một phần thuộc về riêng user (Cloudflare:
// .../accounts/{account_id}/ai/v1), nhưng registry KHÔNG có bước substitution
// nào — chuỗi `{account_id}` được ghép thẳng vào request path, nên user nhận về
// một 404 từ host của provider thay vì một lỗi cấu hình nói đúng thứ họ thiếu.
// Validation duy nhất của registry là `BaseURL != ""`
// (TestProviderPresetsHaveRequiredFields), mà placeholder thì pass check đó.
// ResolveProviderErr gọi hàm này để fail fast, có nêu tên biến còn thiếu.
func unsubstitutedPlaceholder(baseURL string) string {
	open := strings.Index(baseURL, "{")
	if open < 0 {
		return ""
	}
	end := strings.Index(baseURL[open:], "}")
	if end < 0 {
		return ""
	}
	return baseURL[open : open+end+1]
}

// ErrNoProvider là sentinel cho "không resolve được LLM provider nào". Wrap kèm
// hướng dẫn cụ thể (env var nào cần export) để user sửa được ngay.
var ErrNoProvider = errors.New("no LLM provider configured")

// stubProviderName là tên opt-in của stub trong YOLO_PROVIDER (nó không nằm
// trong providerPresets: stub không có BaseURL và không nên xuất hiện trong
// /provider list như một provider thật).
const stubProviderName = "stub"

// stubOptIn báo user có *cố ý* chọn stub không: YOLO_STUB=1 (hoặc true/yes/on)
// hoặc YOLO_PROVIDER=stub. Stub là keyword matcher, không phải model — output
// của nó nhìn từ ngoài không phân biệt được với một model thật, nên nó chỉ được
// chọn khi user nói rõ, không bao giờ là fallback ngầm.
func stubOptIn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("YOLO_STUB"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("YOLO_PROVIDER")), stubProviderName)
}

// ResolveProviderErr là dạng fail-fast của ResolveProvider: YOLO_PROVIDER →
// lookup preset → build từ preset + key của chính preset đó; nếu không set thì
// env-only path (YOLO_BASE_URL + YOLO_API_KEY); nếu không có gì → ErrNoProvider
// kèm hướng dẫn. Stub chỉ trả về khi opt-in (YOLO_STUB=1 / YOLO_PROVIDER=stub).
// Composition root nên dùng hàm này và abort ngay khi có lỗi.
func ResolveProviderErr() (Provider, error) {
	if stubOptIn() {
		return NewStubProvider(128_000), nil
	}
	if name := os.Getenv("YOLO_PROVIDER"); name != "" {
		p, ok := LookupProvider(name)
		if !ok {
			return nil, fmt.Errorf("%w: unknown provider %q — run /provider to list presets, or use YOLO_BASE_URL + YOLO_API_KEY for any OpenAI-compatible endpoint", ErrNoProvider, name)
		}
		if ph := unsubstitutedPlaceholder(p.BaseURL); ph != "" {
			return nil, fmt.Errorf("%w: provider %q's base URL still contains the placeholder %s (%s) — the preset registry has no substitution for it, so that literal text would be sent as part of the request path; set YOLO_BASE_URL to the full URL with %s replaced by your own value and leave YOLO_PROVIDER unset (key via YOLO_API_KEY)", ErrNoProvider, p.Name, ph, p.BaseURL, ph)
		}
		key := resolveAPIKey(p)
		if p.NeedsKey && key == "" {
			// KHÔNG fall through sang env path: nó sẽ build provider cho một
			// endpoint khác (YOLO_BASE_URL, do /provider set thành base-url của
			// preset này) và cấp cho nó một key user không hề định gửi tới đây.
			return nil, fmt.Errorf("%w: provider %q needs an API key — export %s", ErrNoProvider, p.Name, p.KeyEnv)
		}
		return ProviderFromPreset(p, key), nil
	}
	if p := OpenAICompatProviderFromEnv(); p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("%w: set YOLO_PROVIDER=<preset> plus that provider's key env var (run /provider to list), or YOLO_API_KEY (+ optional YOLO_BASE_URL) for any OpenAI-compatible endpoint; to run the deterministic offline stub instead — it is NOT a model — set YOLO_STUB=1", ErrNoProvider)
}

// ResolveProvider giữ chữ ký Provider-only cho composition root (headless +
// tui_runner). Nó không bao giờ trả nil và không bao giờ âm thầm trả stub: khi
// resolve fail nó trả unconfiguredProvider — mọi Stream fail ngay với đúng lỗi
// actionable đó, nên user thấy một error thay vì một câu trả lời giả.
func ResolveProvider() Provider {
	p, err := ResolveProviderErr()
	if err != nil {
		return &unconfiguredProvider{err: err}
	}
	return p
}

// unconfiguredProvider là failure value "ồn ào" của ResolveProvider: nó implement
// Provider nhưng mọi Stream trả về lỗi cấu hình, không bao giờ trả text.
type unconfiguredProvider struct{ err error }

// Window trả default budget để Prompt Compiler vẫn compile được (lỗi nổ ở
// Stream, nơi user thấy được, chứ không phải ở khâu tính budget).
func (u *unconfiguredProvider) Window() int { return 128_000 }

// Stream không bao giờ stream: nó trả thẳng lỗi cấu hình.
func (u *unconfiguredProvider) Stream(context.Context, Request) (<-chan Chunk, error) {
	return nil, u.err
}

// Ensure unconfiguredProvider satisfies the Provider interface at compile time.
var _ Provider = (*unconfiguredProvider)(nil)
