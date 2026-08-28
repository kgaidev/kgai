# Changelog

## 0.2.0

### Added
- **The graph.** `kgai: Graph` draws the live graph on a canvas: elements coloured by
  kind and sized by the decisions that shaped them, links as arrows, contested elements
  ringed in red, and the decisions themselves as a layer to switch on — hanging on the
  elements they shaped, dashed to the ones they replaced. Search with type-ahead, kinds
  as toggles, drag, zoom, pan; hover shows the neighbours; a click opens the node in the
  detail panel. It follows the log like everything else. A **Graph** button sits on every
  page of the detail panel, next to Help, and on the Elements view's toolbar.

### Fixed
- **Charts showed every bar at full width.** The webview's Content Security Policy
  allows only the nonced stylesheet, so the inline `style="width:…%"` that sized each
  bar was dropped and every bar filled its track. Bars are now SVG rectangles sized by
  a width attribute, which the policy allows; a test renders a tally and checks the
  widths are proportional and that no page carries an inline style.
- The reader's exit is no longer logged into a closed output channel while the
  extension host shuts down.

## 0.1.0

First release: decisions (search, exact filters), elements by kind, conflicts and
people in the sidebar; a detail panel with Back; the overview with charts; a status bar
item; the log followed as it changes. The bundled `kgread` reads the store exactly as
`kg` finds it and never touches the graph database.
