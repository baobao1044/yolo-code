// Theme system for the TUI (File 14 §14.11). The original implementation
// hardcoded 14 bright 256-color foreground styles with no background, which is
// low-contrast and eye-straining on light/warm terminal themes. This package
// introduces a swappable Theme with four palettes (dark/light/contrast/mono)
// selected by the YOLO_THEME env var, with NO_COLOR forcing the mono palette.
//
// The palettes use lipgloss.AdaptiveColor where a color should flip between a
// light-bg and dark-bg variant (light theme picks a dark fg; dark theme picks a
// bright fg). High-contrast uses bold white/black for maximum legibility, and
// mono strips all color for accessibility and terminals without color support.
//
// The Theme is a struct of lipgloss.Style fields with the same names the rest
// of the TUI already used (header, state, user, …) so the migration is a
// one-line rename per call site — the styles are accessed as theme.header etc.
// instead of the package-level headerStyle var. A package-level `theme` is
// initialized by loadTheme() at init time so existing code keeps a global
// reference; tests can call loadTheme to build a specific palette.

package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme bundles every lipgloss.Style the TUI renders. Fields line up with the
// former package-level vars (headerStyle → Theme.header, etc.) so call sites
// read theme.<name> instead of <name>Style.
type Theme struct {
	header      lipgloss.Style
	state       lipgloss.Style
	banner      lipgloss.Style
	prompt      lipgloss.Style
	user        lipgloss.Style
	assistant   lipgloss.Style
	thinking    lipgloss.Style
	tool        lipgloss.Style
	observation lipgloss.Style
	reflection  lipgloss.Style
	errorStyle  lipgloss.Style
	success     lipgloss.Style
	warning     lipgloss.Style
	muted       lipgloss.Style
	focus       lipgloss.Style
	unfocus     lipgloss.Style
	sep         lipgloss.Style
	chatPane    lipgloss.Style
	railPane    lipgloss.Style

	// noMotion disables the spinner animation and cursor blink when true
	// (YOLO_NO_MOTION set). Accessibility: motion-sensitive users.
	noMotion bool
}

// ac is a shorthand for an adaptive color that picks a light-bg fg and a
// dark-bg fg. Used by the light palette so a light terminal gets dark text
// and a dark terminal gets a readable bright text.
func ac(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

// darkTheme is the default palette, aligned with the Codex TUI style guide:
// cyan = interactive/status (header, state, focus), magenta = the assistant,
// green = success/additions, red = errors/deletions, amber = warning, dim
// gray = secondary text (thinking, tool, observation). Primary text (user)
// is bold with no explicit foreground — the terminal default. Blue and
// yellow are avoided (no guaranteed contrast); black/white are avoided.
func darkTheme() *Theme {
	cyan := ac("#186f77", "#56c1c1")    // interactive/status indicators
	magenta := ac("#9d4edd", "#c77dff") // the assistant
	red := ac("#c01c28", "#ef4146")     // error red
	green := ac("#1a8f3b", "#3fc26a")   // success green
	amber := ac("#b3541e", "#e09a3a")   // warning amber
	gray := ac("#5a5a5a", "#8a8a8a")    // muted/secondary
	grayBright := ac("#6a6a6a", "#a0a0a0")
	sep := ac("#9a9a9a", "#5a5a5a")
	return &Theme{
		header:      lipgloss.NewStyle().Bold(true).Foreground(cyan),
		state:       lipgloss.NewStyle().Bold(true).Foreground(cyan),
		banner:      lipgloss.NewStyle().Foreground(grayBright),
		prompt:      lipgloss.NewStyle().Foreground(grayBright),
		user:        lipgloss.NewStyle().Bold(true), // primary text = default bold, no color
		assistant:   lipgloss.NewStyle().Foreground(magenta),
		thinking:    lipgloss.NewStyle().Foreground(gray),
		tool:        lipgloss.NewStyle().Foreground(gray),
		observation: lipgloss.NewStyle().Foreground(gray),
		reflection:  lipgloss.NewStyle().Foreground(amber),
		errorStyle:  lipgloss.NewStyle().Foreground(red),
		success:     lipgloss.NewStyle().Foreground(green),
		warning:     lipgloss.NewStyle().Foreground(amber),
		muted:       lipgloss.NewStyle().Foreground(gray),
		focus:       lipgloss.NewStyle().Bold(true).Foreground(cyan),
		unfocus:     lipgloss.NewStyle().Foreground(gray),
		sep:         lipgloss.NewStyle().Foreground(sep),
		chatPane: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			Padding(0, 1),
		railPane: lipgloss.NewStyle().Padding(0, 1),
	}
}

// lightTheme is tuned for light/warm terminal backgrounds: dark text on the
// terminal's light bg. Uses adaptive colors so a dark terminal still gets a
// readable fg, but the light variants are explicitly dark for contrast.
func lightTheme() *Theme {
	cyan := lipgloss.AdaptiveColor{Light: "#186f77", Dark: "#56c1c1"}
	red := lipgloss.AdaptiveColor{Light: "#c01c28", Dark: "#ef7676"}
	green := lipgloss.AdaptiveColor{Light: "#1a8f3b", Dark: "#5fd98a"}
	amber := lipgloss.AdaptiveColor{Light: "#b3541e", Dark: "#e0a83a"}
	gray := lipgloss.AdaptiveColor{Light: "#4a4a4a", Dark: "#9a9a9a"}
	grayBright := lipgloss.AdaptiveColor{Light: "#3a3a3a", Dark: "#b0b0b0"}
	magenta := lipgloss.AdaptiveColor{Light: "#7b2cbf", Dark: "#c77dff"}
	sep := lipgloss.AdaptiveColor{Light: "#7a7a7a", Dark: "#5a5a5a"}
	return &Theme{
		header:      lipgloss.NewStyle().Bold(true).Foreground(cyan),
		state:       lipgloss.NewStyle().Bold(true).Foreground(cyan),
		banner:      lipgloss.NewStyle().Foreground(grayBright),
		prompt:      lipgloss.NewStyle().Foreground(grayBright),
		user:        lipgloss.NewStyle().Bold(true),
		assistant:   lipgloss.NewStyle().Foreground(magenta),
		thinking:    lipgloss.NewStyle().Foreground(gray),
		tool:        lipgloss.NewStyle().Foreground(gray),
		observation: lipgloss.NewStyle().Foreground(gray),
		reflection:  lipgloss.NewStyle().Foreground(amber),
		errorStyle:  lipgloss.NewStyle().Foreground(red),
		success:     lipgloss.NewStyle().Foreground(green),
		warning:     lipgloss.NewStyle().Foreground(amber),
		muted:       lipgloss.NewStyle().Foreground(gray),
		focus:       lipgloss.NewStyle().Bold(true).Foreground(cyan),
		unfocus:     lipgloss.NewStyle().Foreground(gray),
		sep:         lipgloss.NewStyle().Foreground(sep),
		chatPane: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			Padding(0, 1),
		railPane: lipgloss.NewStyle().Padding(0, 1),
	}
}

// contrastTheme maximizes legibility: bold foreground on the default bg, no
// mid-tone grays. For low-vision users and terminals where color is unreliable.
func contrastTheme() *Theme {
	text := lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}
	accent := lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}
	red := lipgloss.AdaptiveColor{Light: "#a00000", Dark: "#ff8080"}
	green := lipgloss.AdaptiveColor{Light: "#006000", Dark: "#80ff80"}
	amber := lipgloss.AdaptiveColor{Light: "#704000", Dark: "#ffd080"}
	sep := lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}
	return &Theme{
		header:      lipgloss.NewStyle().Bold(true).Foreground(accent),
		state:       lipgloss.NewStyle().Bold(true).Foreground(accent),
		banner:      lipgloss.NewStyle().Bold(true).Foreground(text),
		prompt:      lipgloss.NewStyle().Bold(true).Foreground(text),
		user:        lipgloss.NewStyle().Bold(true).Foreground(text),
		assistant:   lipgloss.NewStyle().Bold(true).Foreground(text),
		thinking:    lipgloss.NewStyle().Foreground(text),
		tool:        lipgloss.NewStyle().Foreground(text),
		observation: lipgloss.NewStyle().Bold(true).Foreground(text),
		reflection:  lipgloss.NewStyle().Bold(true).Foreground(amber),
		errorStyle:  lipgloss.NewStyle().Bold(true).Foreground(red),
		success:     lipgloss.NewStyle().Bold(true).Foreground(green),
		warning:     lipgloss.NewStyle().Bold(true).Foreground(amber),
		muted:       lipgloss.NewStyle().Foreground(text),
		focus:       lipgloss.NewStyle().Bold(true).Reverse(true).Foreground(text),
		unfocus:     lipgloss.NewStyle().Foreground(text),
		sep:         lipgloss.NewStyle().Bold(true).Foreground(sep),
		chatPane: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			Padding(0, 1),
		railPane: lipgloss.NewStyle().Padding(0, 1),
	}
}

// monoTheme strips all color (ANSI reset only). For NO_COLOR, terminals without
// color support, and users who prefer monochrome. Distinguished by bold, not
// hue.
func monoTheme() *Theme {
	plain := lipgloss.NewStyle()
	bold := lipgloss.NewStyle().Bold(true)
	return &Theme{
		header:      bold,
		state:       bold,
		banner:      plain,
		prompt:      plain,
		user:        bold,
		assistant:   plain,
		thinking:    plain,
		tool:        plain,
		observation: bold,
		reflection:  bold,
		errorStyle:  bold,
		success:     bold,
		warning:     bold,
		muted:       plain,
		focus:       bold.Reverse(true),
		unfocus:     plain,
		sep:         plain,
		chatPane: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			Padding(0, 1),
		railPane: lipgloss.NewStyle().Padding(0, 1),
	}
}

// loadTheme selects a palette from the YOLO_THEME env var (dark/light/contrast/
// mono, default dark) and forces mono when NO_COLOR is set (any non-empty
// value, per the NO_COLOR spec). YOLO_NO_MOTION sets noMotion to disable the
// spinner/cursor animation.
func loadTheme() *Theme {
	t := darkTheme()
	switch strings.ToLower(strings.TrimSpace(os.Getenv("YOLO_THEME"))) {
	case "light":
		t = lightTheme()
	case "contrast":
		t = contrastTheme()
	case "mono":
		t = monoTheme()
	}
	if os.Getenv("NO_COLOR") != "" {
		t = monoTheme()
	}
	if os.Getenv("YOLO_NO_MOTION") != "" {
		t.noMotion = true
	}
	return t
}

// theme is the package-level palette, initialized once at load. The rest of
// the TUI reads theme.<field> in place of the former package-level style vars.
// setTheme reassigns it (Phase B slash command /theme) — safe because the
// bubbletea Update/View loop is single-threaded, so there's no concurrent
// read/write.
var theme = loadTheme()

// setTheme switches the active palette by name (dark/light/contrast/mono). It
// sets YOLO_THEME, reloads the theme, and returns true on success. Returns
// false (and leaves the env unchanged) for an unknown name. NO_COLOR still
// overrides (mono) and YOLO_NO_MOTION still applies, per loadTheme.
func setTheme(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dark", "light", "contrast", "mono":
	default:
		return false
	}
	_ = os.Setenv("YOLO_THEME", strings.ToLower(strings.TrimSpace(name)))
	theme = loadTheme()
	return true
}

// currentThemeName returns the active theme's name (dark/light/contrast/mono),
// from YOLO_THEME env or "dark" (default). Used by /status.
func currentThemeName() string {
	t := strings.ToLower(strings.TrimSpace(os.Getenv("YOLO_THEME")))
	switch t {
	case "dark", "light", "contrast", "mono":
		return t
	}
	return "dark"
}
