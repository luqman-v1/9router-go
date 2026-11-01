### fix(commandcode): inject reasoning_content placeholder for multi-turn chats

- Injected `reasoning_content: " "` into assistant messages for `commandcode` provider (porting upstream `decolua/9router` PR #5290).
- Updated `buildCommandcodeBody` to map assistant `reasoning_content` into CommandCode reasoning content blocks.
- Added comprehensive unit tests for commandcode reasoning injection and wire-level request building.
