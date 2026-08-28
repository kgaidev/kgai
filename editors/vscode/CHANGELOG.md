# Changelog

## 0.1.0

First release: decisions (search, exact filters), elements by kind, conflicts and
people in the sidebar; a detail panel with Back; the overview with charts; the graph
(elements, links, the decisions as a layer) on a canvas; a status bar item; the log
followed as it changes. The bundled `kgread` reads the store exactly as
`kg` finds it and never touches the graph database.

### Fixed
- **Charts showed every bar at full width.** The webview's Content Security Policy
  allows only the nonced stylesheet, so the inline `style="width:…%"` that sized each
  bar was dropped and every bar filled its track. Bars are now SVG rectangles sized by
  a width attribute, which the policy allows; a test renders a tally and checks the
  widths are proportional and that no page carries an inline style.
