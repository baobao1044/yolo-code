// Tests for the Phase A theme system: loadTheme selects a palette from
// YOLO_THEME (dark/light/contrast/mono, default dark) and forces mono when
// NO_COLOR is set. Each palette is a *Theme with styled fields; we assert
// distinguishing traits (e.g. mono strips color, light uses adaptive colors,
// contrast is bold) rather than exact ANSI bytes.

package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// withEnv sets env vars for a test, then restores. Each call is independent
// (Go runs tests in the same process, so we must restore between cases).
func withEnv(t *testing.T, vars map[string]string, fn func()) {
	t.Helper()
	saved := make(map[string]string)
	for k, v := range vars {
		saved[k] = os.Getenv(k)
		os.Setenv(k, v)
	}
	// Always clear the theme env vars not explicitly set, so a prior test's
	// setting doesn't leak into this one.
	for _, k := range []string{"YOLO_THEME", "NO_COLOR", "YOLO_NO_MOTION"} {
		if _, ok := vars[k]; !ok {
			if prev, had := os.LookupEnv(k); had {
				saved[k] = prev
				os.Unsetenv(k)
			} else {
				saved[k] = ""
			}
		}
	}
	defer func() {
		for k, v := range saved {
			os.Setenv(k, v)
		}
	}()
	fn()
}

// TestLoadThemeDefaultDark asserts the default (no env) is the dark palette.
func TestLoadThemeDefaultDark(t *testing.T) {
	withEnv(t, nil, func() {
		tm := loadTheme()
		// darkTheme's header is bold (non-zero bold); monoTheme's isn't.
		if !tm.header.GetBold() {
			t.Error("default theme header not bold, want darkTheme (bold header)")
		}
	})
}

// TestLoadThemeLight asserts YOLO_THEME=light selects the light palette.
func TestLoadThemeLight(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_THEME": "light"}, func() {
		tm := loadTheme()
		// lightTheme uses an AdaptiveColor for the state field; a plain color
		// returns its Foreground directly. AdaptiveColor is a distinct type.
		fg := tm.state.GetForeground()
		if _, ok := fg.(lipgloss.AdaptiveColor); !ok {
			t.Errorf("light theme state foreground = %T, want lipgloss.AdaptiveColor", fg)
		}
	})
}

// TestLoadThemeContrast asserts YOLO_THEME=contrast selects the contrast palette.
func TestLoadThemeContrast(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_THEME": "contrast"}, func() {
		tm := loadTheme()
		// contrastTheme uses reverse video on the focus tag (high contrast for
		// low-vision users); other palettes don't.
		if !tm.focus.GetReverse() {
			t.Error("contrast theme focus not reverse, want contrastTheme (reverse video focus)")
		}
	})
}

// TestLoadThemeMono asserts YOLO_THEME=mono selects the mono palette (no color).
func TestLoadThemeMono(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_THEME": "mono"}, func() {
		tm := loadTheme()
		// monoTheme's header has no foreground color set (unstyled foreground).
		fg := tm.header.GetForeground()
		if _, ok := fg.(lipgloss.NoColor); !ok {
			t.Errorf("mono theme header foreground = %v, want lipgloss.NoColor (no color)", fg)
		}
	})
}

// TestLoadThemeNoColorOverrides asserts NO_COLOR (any non-empty value per the
// spec) forces mono regardless of YOLO_THEME.
func TestLoadThemeNoColorOverrides(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_THEME": "light", "NO_COLOR": "1"}, func() {
		tm := loadTheme()
		fg := tm.header.GetForeground()
		if _, ok := fg.(lipgloss.NoColor); !ok {
			t.Errorf("NO_COLOR set but theme header foreground = %v, want NoColor (mono)", fg)
		}
	})
}

// TestLoadThemeNoMotion asserts YOLO_NO_MOTION sets the noMotion flag.
func TestLoadThemeNoMotion(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_NO_MOTION": "1"}, func() {
		tm := loadTheme()
		if !tm.noMotion {
			t.Error("YOLO_NO_MOTION set but noMotion = false, want true")
		}
	})
}

// TestLoadThemeNoMotionUnset asserts the default is animated (noMotion false).
func TestLoadThemeNoMotionUnset(t *testing.T) {
	withEnv(t, nil, func() {
		tm := loadTheme()
		if tm.noMotion {
			t.Error("default noMotion = true, want false (animation on)")
		}
	})
}
