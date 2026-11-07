### track live availability and cooldown (supersedes #433)

The gateway now distinguishes a model that is gone for good from one that is
temporarily unavailable:

- A live upstream 410 (model retired) records a permanent `gone` badge and a
  successor when the provider names one. A catalogue sync that re-lists the
  model revives it; `RetainLiveModels` never erases a permanent record.
- A live model-level fault (HTTP 404, a model-scoped 429, or a gateway fault
  500–504) records a transient `cooldown` badge valid for 5 minutes and parks
  the connection for that window. Cooldowns never overwrite or erase a
  permanent `gone`/`retired` record, and `ClearModelDeprecation` / sync
  `RetainLiveModels` do not touch an active cooldown.
- Failures are counted per business request, not per connection attempt, so
  one request that walks several failed accounts records a single cooldown.
- The cooldown condition is written in positive form (no De Morgan lint):
  record when the status is 404, a model-scoped 429, or a gateway fault 500–504.
- The dashboard `ModelDeprecation` status union now includes `cooldown`, and
  badges render "Cooling down" instead of a permanent "Retired" state.
