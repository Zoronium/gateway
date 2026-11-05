---
name: Gateway admin
description: A calm, dense operations console for reading what an AI gateway did, in a near-black blue ground with a matching cool light theme.
colors:
  ground: "#0a0d14"
  surface-1: "#0f141f"
  surface-2: "#161e2e"
  surface-3: "#1c2638"
  hairline: "#1e293b"
  hairline-strong: "#2b3a52"
  text: "#f1f5f9"
  text-muted: "#98a6bb"
  text-faint: "#7d8ba1"
  violet: "#6366f1"
  violet-ink: "#c7caff"
  violet-button: "#5558e8"
  on-violet: "#ffffff"
  cyan: "#22c7e6"
  ok: "#34d399"
  warn: "#fbbf24"
  bad: "#f87171"
  rose: "#fb7185"
  ground-light: "#f2f4f9"
  surface-1-light: "#ffffff"
  surface-2-light: "#eef1f7"
  surface-3-light: "#e4e9f3"
  hairline-light: "#dce2ed"
  hairline-strong-light: "#c3cddf"
  text-light: "#0f172a"
  text-muted-light: "#46546a"
  text-faint-light: "#5b6a80"
  violet-light: "#4f46e5"
  violet-ink-light: "#3730a3"
  cyan-light: "#0e7490"
  ok-light: "#046245"
  warn-light: "#8f420d"
  bad-light: "#b91c1c"
  rose-light: "#be123c"
typography:
  metric:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "24px"
    fontWeight: 600
    lineHeight: 1.15
    letterSpacing: "-0.02em"
    fontFeature: "tnum"
  headline:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "15px"
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: "-0.01em"
  title:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "14px"
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: "-0.01em"
  body:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.5
  table:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "12.5px"
    fontWeight: 400
    lineHeight: 1.5
  label:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "12px"
    fontWeight: 500
    lineHeight: 1.5
  table-head:
    fontFamily: "Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "11px"
    fontWeight: 600
    lineHeight: 1.5
    letterSpacing: "0.05em"
  mono:
    fontFamily: "'JetBrains Mono', ui-monospace, 'Cascadia Mono', 'SF Mono', Menlo, Consolas, monospace"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.5
    fontFeature: "tnum"
rounded:
  xs: "3px"
  control: "4px"
  nav: "6px"
  inset: "6px"
  panel: "8px"
  inspector-inset: "6px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "20px"
  page-x: "24px"
  rail: "224px"
  top-bar: "48px"
  control-height: "32px"
  row-height: "36px"
components:
  button:
    backgroundColor: "{colors.surface-1}"
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    height: "32px"
    padding: "0 12px"
  button-primary:
    backgroundColor: "{colors.violet-button}"
    textColor: "{colors.on-violet}"
    rounded: "{rounded.control}"
    height: "32px"
    padding: "0 12px"
  button-small:
    backgroundColor: "{colors.surface-1}"
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    height: "26px"
    padding: "0 9px"
  button-danger-armed:
    backgroundColor: "{colors.bad}"
    textColor: "{colors.on-violet}"
    rounded: "{rounded.control}"
    height: "32px"
  input:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    height: "32px"
    padding: "0 10px"
  search:
    backgroundColor: "{colors.surface-1}"
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    height: "32px"
    padding: "0 34px 0 32px"
  nav-item:
    textColor: "{colors.text-muted}"
    rounded: "{rounded.nav}"
    height: "34px"
    padding: "0 10px"
  nav-item-current:
    backgroundColor: "{colors.violet}"
    textColor: "{colors.violet-ink}"
    rounded: "{rounded.nav}"
  panel:
    backgroundColor: "{colors.surface-1}"
    textColor: "{colors.text}"
    rounded: "{rounded.panel}"
    padding: "16px"
  metric-strip-cell:
    backgroundColor: "{colors.surface-1}"
    textColor: "{colors.text}"
    padding: "14px 16px 12px"
  badge:
    backgroundColor: "{colors.surface-3}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.control}"
    height: "20px"
    padding: "0 7px"
  chip:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.control}"
    height: "20px"
    padding: "0 6px"
  table-row:
    textColor: "{colors.text}"
    height: "36px"
    padding: "0 12px"
  inspector:
    backgroundColor: "{colors.surface-1}"
    textColor: "{colors.text}"
    width: "440px"
---

# Design System: Gateway admin

## Overview

**Creative North Star: "The Quiet Instrument"**

A calm instrument for reading what a gateway did. The ground is near-black blue, surfaces are two tonal steps above it, and structure comes from hairlines, not shadows. Almost everything is neutral slate; colour appears only when it carries a fact. Because so little else is saturated, the one rose stem standing on the cyan traffic curve is the first thing the eye lands on.

The build is a standard operations console: a 224px left rail, a 48px top bar, a content column, and a right-hand inspector drawer. Navigation and controls are deliberately conventional so that the data is the only unusual thing on screen. Density is high but regular: 36px table rows, 32px controls, 13px body. The system ships a dark main theme and a matching light theme that follows the operating system setting, with the same colour roles in both.

The system rejects the category default of a grid of equal metric cards. Metrics sit in one ruled strip of cells separated by 1px lines, each cell a link to the requests behind it.

**Key Characteristics:**
- Tonal layering and hairlines instead of shadows; the only shadows belong to floating layers (tooltip, inspector).
- Colour is reserved by meaning: violet for action and current location, cyan for throughput data, green/amber/red/rose for state.
- Monospace with tabular figures for ids, model names, times and every number in a table.
- 4px controls, 8px panels, 3 to 6px insets; no pill shapes.
- Every number and every chart stem is a way into the matching requests.

## Colors

A near-black blue ground with slate text and a restrained spectrum that is spent only on meaning. Values below are the dark theme; the `-light` tokens in the frontmatter are the deepened counterparts used under `prefers-color-scheme: light`, with identical roles.

### Primary
- **Electric Violet** (`violet`, #6366f1; button fill `violet-button`, #5558e8): primary action and the current page only. Used as a 12 to 22 percent tint behind the active nav item, the selected range segment and the selected table row, never as a large solid field. Text on a violet tint is **Lavender Ink** (`violet-ink`, #c7caff); links use the same ink. The focus ring is a 2px violet outline with 2px offset.

### Secondary
- **Signal Cyan** (`cyan`, #22c7e6): throughput and latency data only: the traffic line, its area fill (13 percent), the first-byte segment of a latency bar, and default bar fills. It is never used for interface chrome.

### Tertiary (state colours)
- **Emerald** (`ok`, #34d399): ok results, the live pulse, healthy provider bars, the key-created callout.
- **Amber** (`warn`, #fbbf24): warnings: rate-limited, model denied, too large, flagged, provider-error bands on the chart (16 percent fill), mid-range provider error rates.
- **Soft Red** (`bad`, #f87171): provider errors, failed states, the armed destructive button, error banners.
- **Rose** (`rose`, #fb7185): blocked requests specifically: chart stems, the alert metric value, rule-hit bars. Distinct from Soft Red so that "blocked by the filter" never reads as "provider failed".

### Neutral
- **Ground** (`ground`, #0a0d14): page, top bar, input wells, code wells.
- **Surface 1** (`surface-1`, #0f141f): rail, panels, drawer, metric cells, search and segmented controls.
- **Surface 2** (`surface-2`, #161e2e): row and nav hover, chips.
- **Surface 3** (`surface-3`, #1c2638): default badge, bar tracks, skeletons, tooltip.
- **Hairline** (`hairline`, #1e293b) and **Hairline Strong** (`hairline-strong`, #2b3a52): borders and dividers; strong for input borders, tick marks, the drawer edge and the tooltip.
- **Text** (`text`, #f1f5f9) for values and headings, **Muted** (`text-muted`, #98a6bb) for labels and secondary content, **Faint** (`text-faint`, #7d8ba1) for placeholders, table heads, axis text and metadata.

### Named Rules
**The Meaning-Only Colour Rule.** Violet means act or "you are here". Cyan means throughput data. Green, amber, red and rose appear only where they mean ok, warning, failed, blocked. Anything else is slate.

**The Quiet Row Rule.** In long tables an `ok` result is plain muted text; only exceptions get a tinted badge. A screen full of chips hides the one that matters.

**The Tint Rule.** State and selection fills are `color-mix` tints (9 to 22 percent) of the role colour over the surface, not solid fills. The one solid exception is the armed destructive button.

## Typography

**Display Font:** none; the console has no display face.
**Body Font:** Geist (falling back through ui-sans-serif, system-ui, Segoe UI, Roboto, Helvetica Neue, sans-serif)
**Label/Mono Font:** JetBrains Mono (falling back through ui-monospace, Cascadia Mono, SF Mono, Menlo, Consolas, monospace)

**Character:** Neutral, engineered sans for reading and a tabular mono for evidence. Neither font file is embedded; both load only if installed locally and the system stacks apply otherwise (an open decision, see Do's and Don'ts).

### Hierarchy
- **Metric** (600, 24px, 1.15, -0.02em, tabular figures): the six values in the metric strip, with a 13px muted unit suffix.
- **Headline** (600, 15px, -0.01em): page name in the top bar and the brand in the rail.
- **Title** (600, 14px, -0.01em): panel and drawer headings; the status sentence on the overview is 14px with 600 emphasis.
- **Body** (400, 13px, 1.5): default text. Table text is 12.5px.
- **Label** (500, 12px): field labels, legend, metric labels (13px), secondary lines, buttons.
- **Table head** (600, 11px, 0.05em, uppercase, faint): column heads only. Uppercase tracking is a table-head device; it is not used above panels or sections.
- **Mono** (400, 12px, tabular figures): ids, model names, timestamps, scores, chart axis text (10.5px) and keyboard hints (11px).

### Named Rules
**The Evidence Is Mono Rule.** Anything the operator might copy, compare or search for (ids, models, times, numbers in tables) is set in the mono stack with tabular figures and right-aligned when numeric.

**The Tight Heads Rule.** Headings at 14 to 24px take slight negative tracking (-0.01em to -0.02em); small text never does.

## Layout

A fixed app shell: a sticky 224px rail (page links, gateway status at the foot), a sticky 48px top bar (page name, request search with the slash key hint, 1H / 24H / 7D switch, Live toggle, sign out), and a content column capped at 1600px with 24px side padding, 20px vertical gaps and 64px bottom room. The inspector is a fixed 440px drawer (full width on narrow screens) that slides in from the right over the content.

The overview reads top to bottom: the six-cell metric strip, the traffic panel (about 300px tall), then two columns (1.15fr / 1fr) for needs-attention and the provider table. Spacing rhythm is 4, 8, 12, 16, 20, 24px; controls are 32px high (26px small), nav rows 34px, table rows 36px.

Responsive steps: at 1180px the strip goes to three columns and two-column rows stack; at 860px the rail becomes a wrapped horizontal bar above the content, the page name is dropped and search takes a full row; at 700px secondary columns are hidden and long cells truncate harder; at 560px the strip goes to two columns and key-value lists stack. Motion is reduced to none when the system asks.

## Elevation & Depth

Tonal layering with hairlines. Ground, then Surface 1 panels, then Surface 2/3 for hover, chips and tracks; each panel has a 1px hairline border. Depth is not used on resting content.

### Shadow Vocabulary
- **Tooltip** (`box-shadow: 0 8px 20px -6px rgba(0,0,0,.45)`): the chart hover tooltip.
- **Inspector** (`box-shadow: -18px 0 44px -14px rgba(0,0,0,.55)`): the drawer, which sits over live content and needs separation from it.

### Named Rules
**The Flat-Until-Floating Rule.** Surfaces are flat. Only layers that float over other content (tooltip, inspector) carry a shadow, and always with a hairline-strong border as well.

## Shapes

Small, square-leaning geometry that suits dense data. Controls, badges and chips use 4px corners; nav items and wells (code, quoted reasons, callouts, error banners) use 6px; panels, the metric strip and the key-created callout use 8px; micro elements (kbd hints, bar tracks, skeletons) use 2 to 3px. Only the live dot and icon-free dots are circles. Borders are always 1px hairlines. Icons are authored 16px stroke glyphs (1.5px stroke, round caps and joins, `currentColor`) drawn as inline SVG.

## Components

### Buttons
- **Shape:** 4px corners (4px), 32px high (26px small), 12px side padding, weight 500.
- **Default:** Surface 1 fill, hairline-strong border, text colour; hover moves to Surface 2.
- **Primary:** solid violet button fill with white text; hover darkens by mixing 15 percent black. At most one per view.
- **Danger:** muted at rest, turns red text and border on hover; once armed it becomes a solid Soft Red fill with white text.
- **Disabled:** 50 percent opacity.
- **Transitions:** background and border colour, 150ms.

### Segmented control and Live toggle
- **Style:** joined buttons in a 1px bordered, 4px-rounded shell (30px high). Selected segment is a 22 percent violet tint with lavender ink. The Live toggle shows a 7px dot that pulses emerald while on and sits faint when off.

### Navigation
- **Rail item:** 34px, 6px corners, muted 500-weight text with a leading 16px icon. Hover lifts to Surface 2; the current page is a 16 percent violet tint with lavender ink. On narrow screens items wrap into a horizontal bar.

### Inputs / Fields
- **Style:** 32px high, Ground fill, hairline-strong border, 4px corners; the top-bar search sits on Surface 1 with a hairline border, a left search icon and a right-aligned mono `/` hint.
- **Focus:** the global 2px violet outline, 2px offset. Labels are 12px muted above the field; hints are 12px faint below it.

### Panels
- **Corner Style:** 8px. **Background:** Surface 1 with a 1px hairline border. **Header:** 48px minimum, 12px by 16px padding, 14px title, links pushed right in lavender ink. **Body padding:** 16px.

### Tables
- **Style:** 36px rows, hairline row dividers, 12px cell padding, faint uppercase 11px heads, no zebra. Hover and keyboard focus are Surface 2; the selected row is a 12 percent violet tint. Clickable rows open the inspector.

### Badges and chips
- **Badge:** 20px, 4px corners, 11.5px weight 500, a tint of the state colour at 14 percent with solid-colour text (neutral: Surface 3 and muted text). Violet variant for keys.
- **Chip:** 20px mono 11px, Surface 2, hairline border; removable filter chips are 26px with a close button.

### Metric Strip
- A single 8px-rounded block of six cells divided by 1px hairlines, each cell a link: muted label, 24px value, faint delta line against the previous period. Deltas are amber when worse and emerald when better. The blocked value turns rose when non-zero.

### Inspector Drawer
- 440px, Surface 1, hairline-strong left edge, slides in with a 200ms ease-out and is hidden from assistive tech while closed. Holds the request story: a why quote well, definition rows (110px term column), a latency span bar (cyan first-byte segment then a lighter remainder) and action buttons.

### Traffic Chart (signature)
- **Cyan area and line** for total requests, 1.6px stroke, 13 percent fill, with the incomplete final bucket drawn dashed.
- **Blocked stems:** a 1.6px rose vertical line from the baseline with a 3px dot at its tip for every bucket that had blocks. Stem height is scaled to a second, right-hand axis (blocked count) and capped at 62 percent of plot height, so a stem never touches the traffic line's ceiling.
- **Provider-error bands:** full-height amber columns at 16 percent for each bucket with provider errors.
- Two axes (requests left, blocked right), hairline gridlines, mono 10.5px faint labels, a hairline-strong crosshair and a floating tooltip listing requests, answered, blocked, flagged, refused and provider errors.

### Threshold Scale
- A 10px bar divided into neutral, amber and red-tinted zones by the filter thresholds, with a 2px light marker for a request's score.

## Do's and Don'ts

### Do:
- **Do** keep colour meaningful: violet for action and the current page, cyan for throughput data, green/amber/red/rose for state only.
- **Do** build depth from the four surface steps and 1px hairlines; reserve shadows for floating layers.
- **Do** set ids, models, times and table numbers in the mono stack with tabular figures, right-aligned when numeric.
- **Do** show an `ok` result as plain muted text in long tables so exceptions are the only tinted badges.
- **Do** make every number and every stem a link to the matching requests.
- **Do** keep controls at 32px, rows at 36px, corners at 4px (controls) and 8px (panels).
- **Do** give every new colour role a light-theme value in the `prefers-color-scheme: light` block, deepened enough to hold contrast on the cool white ground.
- **Do** honour reduced motion and keep the focus ring visible.

### Don't:
- **Don't** replace the metric strip pattern with a grid of separate cards.
- **Don't** use violet as a large solid field or for data series, or cyan for chrome.
- **Don't** use Soft Red for blocked requests or Rose for provider errors; the two meanings stay separate.
- **Don't** add gradients; the single decorative fill is the flat low-opacity area under the traffic line (the threshold scale is the one functional hard-stop band).
- **Don't** invent data panels the gateway cannot back with real events.
- **Don't** load fonts, icons or images from another origin; icons are inline SVG.
- **Don't** treat the system-font fallbacks as the final typography: embedding Geist and JetBrains Mono is an open decision, and layouts must stay stable under either.
