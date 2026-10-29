---
name: Terminal Mail
description: Paper, graphite, and bitmap window chrome for private iCloud mail.
colors:
  paper: "#E6E6E1"
  paper-2: "#F3F3EF"
  paper-3: "#FCFCFA"
  ink: "#141413"
  ink-2: "#2E2E2C"
  mute: "#6A6A65"
  mute-2: "#9A9A93"
  line: "#2E2E2C"
  hair: "#D0D0C9"
  tint: "#E1E1DA"
  dot: "rgba(20,20,19,.30)"
  acc: "#4A4A46"
  acc-ink: "#F3F3EF"
  acc-mute: "#B4B4AD"
  dark-paper: "#0D0D0C"
  dark-paper-2: "#151514"
  dark-paper-3: "#1C1C1B"
  dark-ink: "#E6E6E0"
  dark-ink-2: "#CACAC3"
  dark-mute: "#8C8C85"
  dark-mute-2: "#5E5E59"
  dark-line: "#8A8A84"
  dark-hair: "#2C2C29"
  dark-tint: "#22221F"
  dark-dot: "rgba(230,230,224,.2)"
  dark-acc: "#B4B4AD"
  dark-acc-ink: "#151514"
  dark-acc-mute: "#5C5C57"
  sig-red: "#E5484D"
  sig-orange: "#F76B15"
  sig-yellow: "#F5B800"
  sig-green: "#30A46C"
  sig-blue: "#0090FF"
  sig-purple: "#8E4EC6"
  sig-gray: "#8B8D98"
typography:
  display:
    fontFamily: "Silkscreen, ui-monospace, monospace"
    fontSize: "52px"
    fontWeight: 400
    lineHeight: 1.04
  headline:
    fontFamily: "Silkscreen, ui-monospace, monospace"
    fontSize: "30px"
    fontWeight: 400
    lineHeight: 1
  title:
    fontFamily: "IBM Plex Mono, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "23px"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  body:
    fontFamily: "IBM Plex Sans, ui-sans-serif, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.65
  app:
    fontFamily: "IBM Plex Sans, ui-sans-serif, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.45
  label:
    fontFamily: "IBM Plex Mono, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "11.5px"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "0.05em"
  field-label:
    fontFamily: "IBM Plex Mono, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "10.5px"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "0.06em"
  counter:
    fontFamily: "Doto, ui-monospace, monospace"
    fontSize: "14px"
    fontWeight: 900
    lineHeight: 1
rounded:
  square: "0px"
spacing:
  pane: "6px"
  tight: "8px"
  control: "10px"
  row: "12px"
  content: "14px"
  section: "16px"
  form: "22px"
  reader: "30px"
components:
  button-primary:
    backgroundColor: "{colors.acc}"
    textColor: "{colors.acc-ink}"
    typography: "{typography.label}"
    rounded: "{rounded.square}"
    padding: "0 12px"
    height: "30px"
  button-secondary:
    backgroundColor: "{colors.paper-3}"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    rounded: "{rounded.square}"
    padding: "0 12px"
    height: "30px"
  button-quiet:
    backgroundColor: "transparent"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    padding: "0 12px"
    height: "30px"
  button-quiet-hover:
    backgroundColor: "{colors.tint}"
  input:
    backgroundColor: "{colors.paper-3}"
    textColor: "{colors.ink}"
    padding: "0 10px"
    height: "36px"
  navigation-active:
    backgroundColor: "{colors.acc}"
    textColor: "{colors.acc-ink}"
    padding: "0 10px"
    height: "26px"
  chip:
    backgroundColor: "{colors.paper-3}"
    textColor: "{colors.ink}"
    padding: "0 7px"
    height: "22px"
  window:
    backgroundColor: "{colors.paper-2}"
    textColor: "{colors.ink}"
    rounded: "{rounded.square}"
  message-row:
    backgroundColor: "{colors.paper-2}"
    padding: "9px 12px 9px 8px"
    height: "60px"
  progress:
    backgroundColor: "{colors.paper-3}"
    padding: "2px"
    height: "22px"
---

# Design System: Terminal Mail

## Overview

**Creative North Star: "Terminal Mail"**

A paper-and-graphite mail workstation with tiled windows, crisp bitmap chrome, and compact technical labels. Reading and composition use open, readable text areas inside the same framed structure. The visual authority is the user-selected native HTML specification, `docs/design/web-client.html` (artifact `mx4gywdnv23g`, version 3).

This records the shipped implementation in `web/src/style.css`, `design.js`, `views.js`, and `fonts.css`, checked against final desktop, phone reader, and phone compose captures in `.impeccable/review/`. The source CSS remains the authority for theme overrides and responsive state combinations.

**Key Characteristics:**
- Square windows with thin frames, striped active titles, and compact controls.
- Monochrome state inversion; optional signal colors only for mail flags.
- Pixel display type and icons paired with readable mail text.
- Crop marks, dither fills, barcode progress, and short stepped motion.

## Colors

Warm near-neutral paper and graphite keep interface state legible in both light and dark themes. Frontmatter keys retain the shipped CSS variable names; `dark-` entries describe their night-theme overrides.

### Primary
- **Graphite** (`acc`): active titles, current rows, selected navigation, primary actions, and selection fill. Pair with `acc-ink`; `acc-mute` carries secondary text within inverted selections.

### Secondary
- **Signal flags** (`sig-red` through `sig-gray`): the seven optional flag colors. With signal colors disabled, the same flag categories use solid, striped, dotted, or checkerboard monochrome patterns. They do not recolor navigation or primary actions.

### Neutral
- **Paper / reading paper / control paper** (`paper`, `paper-2`, `paper-3`): outer canvas, window bodies, and inputs or small raised controls.
- **Ink / secondary ink** (`ink`, `ink-2`): primary text and secondary content.
- **Muted / faint** (`mute`, `mute-2`): metadata and low-emphasis tree connectors; faint is not a substitute for readable body text.
- **Frame / hairline / hover tint** (`line`, `hair`, `tint`): structural borders, internal dividers, and hover or picked rows. `dot` supplies the low-contrast background dot field.

**The Inversion Rule.** Active states use the paired accent background and accent ink; secondary text inside them uses accent muted ink.

## Typography

**Display Font:** Silkscreen, with a monospace fallback. **Body Font:** IBM Plex Sans, with a sans-serif fallback. **Label/Mono Font:** IBM Plex Mono. **Counter Font:** Doto. Fonts are self-hosted through `web/src/fonts.css`; license files ship under `web/public/licenses/`.

### Hierarchy
- **Display:** authentication headline; steps down to 40px on intermediate widths and 34px on phones.
- **Headline:** settings title; 21px on phones.
- **Title:** message subject; 18px in the final phone reader.
- **Body:** reading and composition; mail body is capped at 68ch. The app default is the smaller `app` role. The reading preference can switch mail text to Mono.
- **Label:** uppercase button and window chrome; navigation uses 12.5px Mono, sender names use 13px Mono. Small button and field-label roles remain compact.
- **Counter:** Doto numerals for unread counts and selected status figures; font size varies by placement.

**The Reading Rule.** Keep display lettering in headlines and identity; use Sans for message prose and Mono for interface chrome and structured metadata.

## Layout

The desktop app fills the viewport (`100dvh`) with a three-column pane grid: sidebar (232px), message list (444px), and flexible reader. Pane gutters and outer padding share the `pane` spacing token. A status bar occupies the bottom 26px. Collapsed panes become 30px spines; the list widens to 520px when the sidebar is collapsed. Settings uses sidebar plus content.

At 761–1200px, the normal columns narrow to 200px and 340px before the flexible reader. At 760px and below, show one mail pane, preserve 6px horizontal inset, and reserve at least 50px above the window for the phone safe area. The list has four bottom tabs (76px); reader and compose hide them. Reader reserves at least 30px below the pane; full-screen compose uses the same top and bottom safe-area limits. Phone overflow controls replace crowded desktop filter and action chrome.

Rows are 60px on desktop and 64px on phones; compact and comfortable preferences use 56px and 74px respectively. Settings rows become a single stacked column on phones. Spacing is deliberately dense rather than a strict mathematical scale: use the recorded recurring steps for pane, control, row, form, and reader contexts.

Authentication pairs a crop-mark headline with a separate 420px form window on desktop; it stacks the two blocks on phones. Compose is a floating 580×540px window on desktop and a dedicated viewport window on phones.

## Elevation & Depth

Depth comes from paper tones, explicit borders, pattern fills, and hard offset shadows native to this approved bitmap window world. Tiled panes stay flat; floating windows carry the stronger shadow. Do not replace the established hard edges with ambient blur.

### Shadow Vocabulary
- **Button hover:** `2px 2px 0 var(--line)` with a one-pixel upward-left translation; pressing removes the shadow and shifts one pixel down-right.
- **Input focus:** `3px 3px 0 var(--acc)`.
- **Floating window / open reply:** `5px 5px 0 var(--line)`.
- **Drag ghost / phone compose action:** `4px 4px 0 var(--line)`.

**The Window Rule.** Use framed paper surfaces for persistent panes and hard offset shadows for floating or interactive elevation.

## Shapes

Keep square corners, including avatars, checkboxes, switches, chips, and window controls. Structural frames are 1.5px; internal rules are 1px. Crop-mark corners use short 18px rules rather than a continuous card border. Title stripes and QuickDraw-style dots, checkerboards, and bars use crisp CSS fills. Icons are the native 9×9 bitmap grids rendered as inline SVG rectangles with `crispEdges`; reuse these silhouettes.

## Components

### Buttons

Compact uppercase Mono controls with square frames. Primary uses inversion; secondary uses control paper; quiet controls use transparent borders and hover tint. Small and large variants are 24px and 40px high. Standard hover and press use the offset-shadow behavior above. Global keyboard focus uses a 2px accent outline with 2px offset; disabled controls use reduced opacity. Bracketed text links invert on hover. Destructive buttons use a dashed border.

### Inputs / Fields

Framed control paper with Mono entry text and small uppercase labels. A focused field gains the hard focus shadow; errors use dashed framing and an adjacent message. Compose fields instead use unboxed inline inputs separated by hairlines. Message editor text uses the body role with a visible focus outline.

### Navigation

A compact Mono tree with bitmap icons, hover tint, inverted current item, and Doto unread counts. Sidebar sections describe actual folder or settings groups. Phone tabs use four equal columns with small icons and labels; the current icon gets an accent square. The reader supplies an Inbox return action in place of bottom tabs.

### Chips

Small square tags for search tokens, recipients, states, and rule operators. Default uses control paper with a thin frame; selected or structured tokens use inversion; dashed chips represent muted or add states. Search tokens wrap when needed.

### Cards / Containers

Windows are the principal container: paper body, structural frame, 26px title bar, and optional minimize/maximize/close controls. Active titles invert and fill their remaining width with fine racing stripes. Attachment cards use the same frame with a separate preview and metadata strip. Avoid introducing a second generic card style.

### Message Rows

A two-line conversation row combines Mono sender/time metadata with Sans subject and snippet. Unread state uses a square marker and stronger sender/subject weight; current state inverts; hover and multi-selection use tint. Desktop quick actions appear on hover. Phone archive is exposed by the existing 104px swipe reveal.

### Progress / Motion

Barcode fills, dithered boundaries, and Doto percentages convey progress. Thin bars support quota and timed feedback. Window reveal uses a vertical clip in four steps (220ms); buttons snap (80ms), short transitions use 140ms, and base feedback uses 200ms. System reduced-motion and the explicit reduced preference suppress repeated animation and transitions. Preserve actual status text beside decorative activity patterns.

## Do's and Don'ts

### Do:
- **Do** pair accent, accent ink, and accent muted ink for every selected state.
- **Do** preserve square frames, inline bitmap SVG icons, and the existing paper hierarchy.
- **Do** keep long mail prose readable with the body role and the existing line-length cap.
- **Do** honor phone safe areas, pane visibility, keyboard focus, and reduced-motion preferences.

### Don't:
- **Don't** spread opt-in flag colors across primary actions or navigation.
- **Don't** replace the established square windows with rounded cards or blurred floating shadows.
- **Don't** use Silkscreen for long message bodies or substitute Unicode symbols for the bitmap icon set.
- **Don't** turn faint metadata colors or tiny decorative text into general-purpose reading styles.
