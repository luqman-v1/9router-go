### fix(github): refresh Copilot fallback model catalog

The static Copilot fallback list still carried retired models
(gpt-5.2/-codex, opus-4.5/4.7, sonnet-4.5, gemini-2.5-pro,
grok-code-fast-1, oswe-vscode-prime, goldeneye-free-auto) and missed
everything Copilot has shipped since. The fallback default is now
gpt-5-mini, and the list matches GitHub's supported-models documentation
and the Copilot /models catalog as of 2026-10-19: current GPT-5.5/5.6
and GPT-6 lines, the Claude Fable/Opus/Sonnet sets, Gemini 3.7/3.8/4
Argon, Kimi K3 and MAI-Code-1.1-Flash. Embeddings stay.

Live account discovery remains authoritative; this only refreshes the
static seed (upstream decolua/9router dbdbb648).
