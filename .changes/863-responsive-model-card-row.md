### fix(dashboard): wrap model path badge and action buttons on narrow mobile viewports (#863)

- On mobile viewports (e.g. Android devices at 360–390px), compatible node model cards overflowed horizontally past the card border when model paths were long and action icons (copy, test latency, visibility toggle) were present.
- Wrapped model path badge container with `flex-wrap` and bounded code badge with `max-w-[72vw] sm:max-w-[360px] truncate`, preventing card edge clipping on mobile displays.
