# 9. Hand-written SVG charts instead of a chart library

Status: proposed

## Context
The dashboard needs bar, line, stacked bar and sparkline charts in Echoo's own palette, with a
small bundle.

## Decision
Build these four as small React SVG components with shared scales and accessible table
fallbacks.

## Consequences
Full control over colour, typography and density; no chart library in the bundle. We own axis
tick logic and tooltips, which is limited and testable.
