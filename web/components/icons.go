package components

// glyphs are the console's icons: the inner shapes of Lucide's 24x24 outline
// set, v0.460.0 (lucide-static), keyed by Lucide's own names so a glyph can be
// traced back to the set and a new one added from the same release.
//
// VENDORED AS DATA, NOT AS A PACKAGE. The repository has no JavaScript
// toolchain and a sprite file would not take `currentColor`, so the shapes are
// copied here and drawn inline by Icon. The licence is LICENSE-lucide, beside
// this file; icons_test.go holds that every entry is only shapes, because they
// are written into the page unescaped.
//
// Only the glyphs a screen draws. A set of fifteen hundred where twenty-seven
// are used is fifteen hundred nobody reviewed.
var glyphs = map[string]string{
	"check":          `<path d="M20 6 9 17l-5-5"/>`,
	"chevron-down":   `<path d="m6 9 6 6 6-6"/>`,
	"chevron-right":  `<path d="m9 18 6-6-6-6"/>`,
	"clock":          `<circle cx="12" cy="12" r="10"/> <polyline points="12 6 12 12 16 14"/>`,
	"cloud":          `<path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"/>`,
	"copy":           `<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/> <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`,
	"database":       `<ellipse cx="12" cy="5" rx="9" ry="3"/> <path d="M3 5V19A9 3 0 0 0 21 19V5"/> <path d="M3 12A9 3 0 0 0 21 12"/>`,
	"download":       `<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/> <polyline points="7 10 12 15 17 10"/> <line x1="12" x2="12" y1="15" y2="3"/>`,
	"external-link":  `<path d="M15 3h6v6"/> <path d="M10 14 21 3"/> <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`,
	"folder":         `<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>`,
	"hard-drive":     `<line x1="22" x2="2" y1="12" y2="12"/> <path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/> <line x1="6" x2="6.01" y1="16" y2="16"/> <line x1="10" x2="10.01" y1="16" y2="16"/>`,
	"history":        `<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/> <path d="M3 3v5h5"/> <path d="M12 7v5l4 2"/>`,
	"info":           `<circle cx="12" cy="12" r="10"/> <path d="M12 16v-4"/> <path d="M12 8h.01"/>`,
	"key-round":      `<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/> <circle cx="16.5" cy="7.5" r=".5" fill="currentColor"/>`,
	"layout-grid":    `<rect width="7" height="7" x="3" y="3" rx="1"/> <rect width="7" height="7" x="14" y="3" rx="1"/> <rect width="7" height="7" x="14" y="14" rx="1"/> <rect width="7" height="7" x="3" y="14" rx="1"/>`,
	"menu":           `<line x1="4" x2="20" y1="12" y2="12"/> <line x1="4" x2="20" y1="6" y2="6"/> <line x1="4" x2="20" y1="18" y2="18"/>`,
	"moon":           `<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>`,
	"panel-left":     `<rect width="18" height="18" x="3" y="3" rx="2"/> <path d="M9 3v18"/>`,
	"play":           `<polygon points="6 3 20 12 6 21 6 3"/>`,
	"plug":           `<path d="M12 22v-5"/> <path d="M9 8V2"/> <path d="M15 8V2"/> <path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>`,
	"refresh-cw":     `<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/> <path d="M21 3v5h-5"/> <path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/> <path d="M8 16H3v5"/>`,
	"rows-3":         `<rect width="18" height="18" x="3" y="3" rx="2"/> <path d="M21 9H3"/> <path d="M21 15H3"/>`,
	"search":         `<circle cx="11" cy="11" r="8"/> <path d="m21 21-4.3-4.3"/>`,
	"sun":            `<circle cx="12" cy="12" r="4"/> <path d="M12 2v2"/> <path d="M12 20v2"/> <path d="m4.93 4.93 1.41 1.41"/> <path d="m17.66 17.66 1.41 1.41"/> <path d="M2 12h2"/> <path d="M20 12h2"/> <path d="m6.34 17.66-1.41 1.41"/> <path d="m19.07 4.93-1.41 1.41"/>`,
	"square":         `<rect width="18" height="18" x="3" y="3" rx="2"/>`,
	"table":          `<path d="M12 3v18"/> <rect width="18" height="18" x="3" y="3" rx="2"/> <path d="M3 9h18"/> <path d="M3 15h18"/>`,
	"tag":            `<path d="M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z"/> <circle cx="7.5" cy="7.5" r=".5" fill="currentColor"/>`,
	"terminal":       `<polyline points="4 17 10 11 4 5"/> <line x1="12" x2="20" y1="19" y2="19"/>`,
	"triangle-alert": `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/> <path d="M12 9v4"/> <path d="M12 17h.01"/>`,
	"workflow":       `<rect width="8" height="8" x="3" y="3" rx="2"/> <path d="M7 11v4a2 2 0 0 0 2 2h4"/> <rect width="8" height="8" x="13" y="13" rx="2"/>`,
	"x":              `<path d="M18 6 6 18"/> <path d="m6 6 12 12"/>`,
}
