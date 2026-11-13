### fix(combo): remove duplicate connection accounts from model picker list

- Clean up model picker groups in Combo creator to list only genuine catalog providers and provider nodes (`provider/model`), eliminating redundant duplicate groups for each active connection account (`connectionName/model`).
- Account management, rotation, and priority remain configured via the provider detail settings.
