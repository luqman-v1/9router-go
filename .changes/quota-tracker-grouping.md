### feat(quota): group the Quota Tracker rows by provider

- The tracker rendered every connection as one flat list. Rows are now folded into collapsible provider sections (icon, label, account count); the first provider opens by default and the rest start collapsed. The provider/account filters, sorting and pagination are unchanged.
- `groupConnectionsByProvider` keeps the caller's row order inside each section and the first-seen provider order, so a grouped list reads the same way as the flat one.
