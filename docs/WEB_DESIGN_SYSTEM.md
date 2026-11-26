# ProjectLens Web Console design system

**ProjectLens — See beyond the code.**

## Pass 1. Establish the design direction

Follow the Anthropic `frontend-design` approach from the Phase 1 brief. The design direction is **Editorial Design × Developer Tools**. The console brings together project repositories, indexed source, Markdown documents, Git history, and operational status. Use the spacing and typography of an editorial index on Global Home and the alignment and density of a developer tool inside a project.

The optical **Lens** is the visual anchor. Concentric aperture rings frame a crosshair with registration ticks. Use a single ink stroke, open space to suggest clear glass planes, and careful alignment to convey precision observation. The same geometry serves the masthead, the larger Home heading mark, and the favicon. Hide the SVG from assistive technology beside the wordmark. Give it an accessible name when it carries standalone meaning.

### Color

| CSS token | Value | Role |
| --- | --- | --- |
| `--lens-paper` | `#FFFFFF` | Primary reading canvas and input surface |
| `--lens-rail` | `#F5F6F7` | Workspace rail and subtle structured bands |
| `--lens-ink` | `#121316` | Primary text, active rules, and focal mark |
| `--lens-graphite` | `#5A606B` | Secondary prose, metadata, and muted labels |
| `--lens-rule` | `#DFE2E7` | Crisp 1px structural rules and table dividers |
| `--lens-focus` | `#2C3038` | Pressed surfaces and high-contrast focus outlines |

Use desaturated ink-on-tint colors for actual system states and change indicators. Accompany color with a visible label or change symbol. Repository sources and roles use neutral badges.

| Meaning | Ink token | Ink value | Tint token | Tint value |
| --- | --- | --- | --- | --- |
| Healthy or added | `--lens-healthy-ink` | `#1E5631` | `--lens-healthy-tint` | `#EEF5F0` |
| Error or deleted | `--lens-error-ink` | `#8B2635` | `--lens-error-tint` | `#FAF0F1` |
| Warning, running, or conflict | `--lens-warning-ink` | `#7A4E0B` | `--lens-warning-tint` | `#FBF6EC` |

### Typography and layout

| Role | Stack | Use |
| --- | --- | --- |
| Display | `'Newsreader', Georgia, serif` | Brand masthead, top-level page titles, and Markdown H1/H2 only |
| UI and tables | `'IBM Plex Sans', -apple-system, BlinkMacSystemFont, sans-serif` | Navigation, controls, prose, and data with `font-variant-numeric: tabular-nums` |
| Code | `'IBM Plex Mono', ui-monospace, SFMono-Regular, monospace` | Code, paths, SHAs, line numbers, API routes, and diagnostic identifiers |

Load the three typefaces through Google Fonts in `index.html` with `display=swap`. The fallback stacks preserve readability when the console runs offline. Use aligned columns, horizontal rules, and whitespace for hierarchy. Center Global Home within a 1120px maximum width (equivalent to `max-w-[1120px]`). Use a 228px fixed-width left rail in the Project Workspace, separated by a single 1px rule. On narrow screens, place its navigation above the main pane and let the links wrap. Keep wide tables and code horizontally scrollable within the pane.

### Global Home wireframe

```text
+--------------------------------------------------------------------------+
| (Lens) ProjectLens                              Projects  Jobs  Settings |
+--------------------------------------------------------------------------+

  +--------------------------------------------------------------------+
  |                                                                    |
  | Projects                                               (Lens mark) |
  | See beyond the code.                                               |
  |--------------------------------------------------------------------|
  | Project list                                      [Create project] |
  | Name              Description              Status          Updated  |
  |--------------------------------------------------------------------|
  | Project row       Description              Status             Date |
  |--------------------------------------------------------------------|
  | Showing range                                    [Previous] [Next] |
  +--------------------------------------------------------------------+
                       1120px maximum width
```

### Project Workspace wireframe

```text
+--------------------------------------------------------------------------+
| (Lens) ProjectLens                              Projects  Jobs  Settings |
+---------------------+----------------------------------------------------+
| All projects        | Overview                                           |
| Project name or ID  | Project summary                                     |
|                     |----------------------------------------------------|
| Overview            | Repositories                                       |
| Repositories        | Name                          Role      Last sync  |
| Search              |----------------------------------------------------|
| Documents           | Documents                                          |
| Activity            |----------------------------------------------------|
|                     | Sync policies and recent jobs                      |
+---------------------+----------------------------------------------------+
      228px rail                         Flexible pane
```

The wireframes show the intended feature placement. Phase 1 implements mastheads, navigation, and page headings.

## Pass 2. Critique and refine

- **Palette.** Exclude warm cream (`#F4F1EA`), terracotta accents, and generic black fills such as pure `#0B0B0B` or `#111`. Keep reading surfaces white and reserve status tints for data that conveys a state.
- **Hierarchy.** Remove uniform rounded SaaS card grids, soft grey shadows, gradients, glows, and glassmorphism. The lens uses geometry and negative space. Use whitespace on Home and compact rows and aligned metadata inside a project.
- **Labels.** Remove tracked ALL-CAPS eyebrows, middle-dot metadata strings such as `A · B · C`, `WORD — fragment` labels, trailing `→` on links or buttons, and decorative `01 / 02 / 03` numbering. Use separate fields for metadata and reserve numbers for content with an actual order or quantity.
- **Interaction.** Use hairline controls with visible labels and inline error descriptions. Keep focus visible on the current control and respect reduced-motion preferences. Name modal dialogs, give destructive confirmations an impact summary, and focus Cancel on open. Include text labels for pagination and announce range changes.
- **Copy.** Use active voice, sentence case, and plain verbs such as “Create project,” “Associate repository,” and “Retry.” Give empty states a concrete next step such as “Associate a repository to inspect its files.” Put the recovery action beside the error and show the server’s error code and request ID when available. Omit apologetic filler, decorative badges, and repeated section descriptions.
