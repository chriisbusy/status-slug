package theme

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// HuhTheme builds a complete huh theme from a palette — every field style
// mapped, so no huh-default (catppuccin) styling leaks through anywhere.
// Shared by the wizard and the dashboard's settings/meter forms.
func HuhTheme(p Palette) huh.Theme {
	base := huh.ThemeBase(true)

	fg := lipgloss.Color(p[Fg])
	muted := lipgloss.Color(p[Muted])
	accent := lipgloss.Color(p[Accent])
	ok := lipgloss.Color(p[OK])
	errC := lipgloss.Color(p[Err])
	border := lipgloss.Color(p[BoxBorder])
	selBg := lipgloss.Color(p[SelectedBg])
	selFg := lipgloss.Color(p[SelectedFg])
	keyHint := lipgloss.Color(p[KeyHint])

	base.Form.Base = base.Form.Base.Foreground(fg)
	base.Group.Base = base.Group.Base.Foreground(fg)
	base.Group.Title = base.Group.Title.Foreground(accent).Bold(true)
	base.Group.Description = base.Group.Description.Foreground(muted)
	base.FieldSeparator = base.FieldSeparator.Foreground(border)

	style := func(fs *huh.FieldStyles, focused bool) {
		if focused {
			fs.Base = fs.Base.Foreground(fg)
			fs.Title = fs.Title.Foreground(accent).Bold(true)
		} else {
			// Blurred fields stay quiet: muted title, no accent, so exactly
			// one field reads as active.
			fs.Base = fs.Base.Foreground(muted)
			fs.Title = fs.Title.Foreground(muted).Bold(false)
		}
		fs.Description = fs.Description.Foreground(muted)
		fs.ErrorIndicator = fs.ErrorIndicator.Foreground(errC)
		fs.ErrorMessage = fs.ErrorMessage.Foreground(muted)
		fs.SelectSelector = fs.SelectSelector.Foreground(accent)
		if focused {
			fs.Option = fs.Option.Foreground(fg)
		} else {
			fs.Option = fs.Option.Foreground(muted)
		}
		fs.NextIndicator = fs.NextIndicator.Foreground(muted)
		fs.PrevIndicator = fs.PrevIndicator.Foreground(muted)
		fs.Directory = fs.Directory.Foreground(accent)
		fs.File = fs.File.Foreground(fg)
		fs.MultiSelectSelector = fs.MultiSelectSelector.Foreground(accent)
		fs.SelectedOption = fs.SelectedOption.Foreground(ok)
		fs.SelectedPrefix = fs.SelectedPrefix.Foreground(ok)
		fs.UnselectedOption = fs.UnselectedOption.Foreground(muted)
		fs.UnselectedPrefix = fs.UnselectedPrefix.Foreground(muted)
		fs.TextInput.Prompt = fs.TextInput.Prompt.Foreground(accent)
		if focused {
			fs.TextInput.Text = fs.TextInput.Text.Foreground(fg)
			fs.TextInput.CursorText = fs.TextInput.CursorText.Foreground(fg)
		} else {
			fs.TextInput.Text = fs.TextInput.Text.Foreground(muted)
			fs.TextInput.CursorText = fs.TextInput.CursorText.Foreground(muted)
		}
		fs.TextInput.Placeholder = fs.TextInput.Placeholder.Foreground(muted)
		// Block cursor: accent surface, readable glyph on it.
		fs.TextInput.Cursor = fs.TextInput.Cursor.Background(accent).Foreground(selFg)
		fs.Card = fs.Card.BorderForeground(border)
		fs.NoteTitle = fs.NoteTitle.Foreground(accent).Bold(true)
		fs.Next = fs.Next.Foreground(accent)
		if focused {
			fs.FocusedButton = fs.FocusedButton.Background(accent).Foreground(selFg).Bold(true)
			fs.BlurredButton = fs.BlurredButton.Foreground(muted)
		} else {
			fs.FocusedButton = fs.FocusedButton.Background(selBg).Foreground(selFg)
			fs.BlurredButton = fs.BlurredButton.Foreground(muted)
		}
	}
	style(&base.Focused, true)
	style(&base.Blurred, false)

	base.Help.ShortKey = base.Help.ShortKey.Foreground(keyHint)
	base.Help.ShortDesc = base.Help.ShortDesc.Foreground(muted)
	base.Help.FullKey = base.Help.FullKey.Foreground(keyHint)
	base.Help.FullDesc = base.Help.FullDesc.Foreground(muted)

	return themeFunc{styles: base}
}

type themeFunc struct{ styles *huh.Styles }

func (t themeFunc) Theme(isDark bool) *huh.Styles { return t.styles }
