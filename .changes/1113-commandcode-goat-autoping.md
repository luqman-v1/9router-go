### feat(quota): auto-ping for the Command Code Provider API

- The Quota Tracker offered auto-ping only for `claude` and `codex`. Command Code publishes the same rolling 5h window (`windowLimits.fiveHour`), so `commandcode-goat` is now in the scheduler's provider set: `commandcode-goatAutoPing` holds its switches and schedules, and the row shows the bolt/schedule controls without requiring an OAuth credential (Command Code authenticates with a bearer key).
- The ping sends the cheap OpenAI-lane model `deepseek/deepseek-v4.1-flash` with the plain one-token body, so it starts the window rather than spending it.
