export interface TTSProviderConfig {
  hasLanguageDropdown?: boolean
  hasModelSelector?: boolean
  hasBrowseButton?: boolean
  hasVoiceIdInput?: boolean
  hasStyleInput?: boolean
  hasLanguageHint?: boolean
  languageOptions?: string[]
  voiceSource?: 'hardcoded' | 'api-language' | 'config' | 'manual'
  modelKey?: string
  voiceKey?: string
  voicesPerModel?: boolean
  apiEndpoint?: string
  defaultVoiceId?: string
}

export const TTS_PROVIDER_CONFIG: Record<string, TTSProviderConfig> = {
  'google-tts': {
    hasLanguageDropdown: false,
    hasModelSelector: false,
    hasBrowseButton: true,
    voiceSource: 'hardcoded',
  },
  'openai': {
    hasLanguageDropdown: false,
    hasModelSelector: true,
    hasBrowseButton: false,
    voiceSource: 'hardcoded',
    modelKey: 'openai-tts-models',
    voiceKey: 'openai-tts-voices',
    voicesPerModel: true,
  },
  'openrouter': {
    hasLanguageDropdown: false,
    hasModelSelector: true,
    hasBrowseButton: false,
    voiceSource: 'hardcoded',
    modelKey: 'openrouter-tts-models',
    voiceKey: 'openrouter-tts-voices',
    voicesPerModel: true,
  },
  'elevenlabs': {
    hasLanguageDropdown: false,
    hasModelSelector: true,
    hasBrowseButton: true,
    hasVoiceIdInput: true,
    voiceSource: 'api-language',
    modelKey: 'elevenlabs-tts-models',
    apiEndpoint: '/api/media-providers/tts/elevenlabs/voices',
  },
  'edge-tts': {
    hasLanguageDropdown: false,
    hasModelSelector: false,
    hasBrowseButton: true,
    voiceSource: 'api-language',
  },
  'selfhosted-tts': {
    hasModelSelector: true,
    hasVoiceIdInput: true,
    hasBrowseButton: false,
    // model and voice are typed by hand; the connected server decides what exists
    voiceSource: 'manual',
  },
  'local-device': {
    hasLanguageDropdown: false,
    hasModelSelector: false,
    hasBrowseButton: true,
    voiceSource: 'api-language',
  },
  'nvidia': {
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: true,
    voiceSource: 'config',
  },
  'hyperbolic': {
    hasModelSelector: true,
    hasBrowseButton: false,
    voiceSource: 'config',
  },
  'deepgram': {
    hasModelSelector: false,
    hasBrowseButton: true,
    voiceSource: 'api-language',
    apiEndpoint: '/api/media-providers/tts/deepgram/voices',
  },
  'huggingface': {
    hasModelSelector: true,
    hasBrowseButton: false,
    voiceSource: 'config',
  },
  'cartesia': {
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: true,
    voiceSource: 'config',
  },
  'playht': {
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: true,
    voiceSource: 'config',
  },
  'coqui': {
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: true,
    voiceSource: 'config',
  },
  'tortoise': {
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: true,
    voiceSource: 'config',
  },
  'inworld': {
    hasModelSelector: true,
    hasBrowseButton: true,
    hasVoiceIdInput: true,
    voiceSource: 'api-language',
    modelKey: 'inworld-tts-models',
    apiEndpoint: '/api/media-providers/tts/inworld/voices',
  },
  'qwen': {
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: true,
    voiceSource: 'config',
  },
  'minimax': {
    hasModelSelector: true,
    hasBrowseButton: true,
    hasVoiceIdInput: true,
    voiceSource: 'api-language',
    apiEndpoint: '/api/media-providers/tts/minimax/voices',
    defaultVoiceId: 'English_expressive_narrator',
  },
  'minimax-cn': {
    hasModelSelector: true,
    hasBrowseButton: true,
    hasVoiceIdInput: true,
    voiceSource: 'api-language',
    apiEndpoint: '/api/media-providers/tts/minimax/voices?provider=minimax-cn',
    defaultVoiceId: 'English_expressive_narrator',
  },
  'gemini': {
    hasLanguageDropdown: false,
    hasLanguageHint: true,
    hasModelSelector: true,
    hasBrowseButton: false,
    voiceSource: 'hardcoded',
    modelKey: 'gemini-tts-models',
    voiceKey: 'gemini-tts-voices',
    voicesPerModel: true,
  },
  'xiaomi-mimo': {
    hasLanguageDropdown: false,
    hasModelSelector: true,
    hasBrowseButton: false,
    hasVoiceIdInput: false,
    hasStyleInput: true,
    hasLanguageHint: true,
    languageOptions: ['Chinese', 'English'],
    voiceSource: 'hardcoded',
    modelKey: 'xiaomi-mimo-tts-models',
    voicesPerModel: true,
  },
}
