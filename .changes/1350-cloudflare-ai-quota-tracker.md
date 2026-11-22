### feat(dashboard): add Cloudflare Workers AI quota tracker (#1350, Closes #1266)

- Cloudflare AI connections now appear in the Quota Tracker instead of being silently excluded.
- Daily neuron usage is read from Cloudflare's GraphQL analytics API and shown against the 10k free-tier allowance; the limit can be overridden with `providerSpecificData.dailyNeuronLimit`.
- Request count is reported as unlimited, with a clear permission message when the token lacks AI Analytics access.
