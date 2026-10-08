import type { ProviderConnection } from '../../api/client'

export type MediaKind = 'embedding' | 'image' | 'tts' | 'stt' | 'video' | 'systemone' | 'webSearch' | 'webFetch'

export interface MediaKindInfo {
  kind: MediaKind
  title: string
  singular: string
  description: string
  emptyMessage: string
}

export const MEDIA_KIND_INFO: Record<MediaKind, MediaKindInfo> = {
  embedding: {
    kind: 'embedding',
    title: 'Embedding Models & Providers',
    singular: 'Embedding',
    description: 'High-dimensional vector embedding models for semantic search and retrieval.',
    emptyMessage: 'No embedding providers found.',
  },
  image: {
    kind: 'image',
    title: 'Text to Image Models & Providers',
    singular: 'Text to Image',
    description: 'Diffusion and generative image creation models and image generation endpoints.',
    emptyMessage: 'No text-to-image providers found.',
  },
  tts: {
    kind: 'tts',
    title: 'Text To Speech Models & Providers',
    singular: 'Text To Speech',
    description: 'Neural voice synthesis, audio generation, and speech rendering endpoints.',
    emptyMessage: 'No text-to-speech providers found.',
  },
  stt: {
    kind: 'stt',
    title: 'Speech To Text Models & Providers',
    singular: 'Speech To Text',
    description: 'Speech recognition, audio transcription, and multilingual captioning endpoints.',
    emptyMessage: 'No speech-to-text providers found.',
  },
  video: {
    kind: 'video',
    title: 'Video Generation Models & Providers',
    singular: 'Video',
    description: 'Text-to-video generation, video extension, and motion synthesis models.',
    emptyMessage: 'No video generation providers found.',
  },
  systemone: {
    kind: 'systemone',
    title: 'System One Models & Providers',
    singular: 'System One',
    description: 'Structured state evaluation models with yes/no question answering.',
    emptyMessage: 'No System One providers found.',
  },
  webSearch: {
    kind: 'webSearch',
    title: 'Web Search Providers',
    singular: 'Web Search',
    description: 'Web search APIs and live search endpoints.',
    emptyMessage: 'No web search providers found.',
  },
  webFetch: {
    kind: 'webFetch',
    title: 'Web Fetch Providers',
    singular: 'Web Fetch',
    description: 'URL content extraction, scraper, and web reader endpoints.',
    emptyMessage: 'No web fetch providers found.',
  },
}

export interface MediaProviderStats {
  total: number
  active: number
  errorCount: number
  status: 'connected' | 'ready' | 'error' | 'none' | 'disabled'
  connections: ProviderConnection[]
}

export function getMediaProviderStats(
  connections: ProviderConnection[],
  providerId: string,
  noAuth?: boolean
): MediaProviderStats {
  const list = connections.filter((c) => c.provider === providerId)
  const activeList = list.filter((c) => c.isActive === 1)
  const errorList = list.filter((c) => c.testStatus === 'error' || (c.testStatus !== 'active' && c.testStatus !== 'passed' && Boolean(c.lastError)))
  const allDisabled = list.length > 0 && activeList.length === 0

  let status: 'connected' | 'ready' | 'error' | 'none' | 'disabled' = 'none'
  if (errorList.length > 0) {
    status = 'error'
  } else if (allDisabled) {
    status = 'disabled'
  } else if (activeList.length > 0) {
    status = 'connected'
  } else if (noAuth) {
    status = 'ready'
  } else {
    status = 'none'
  }

  return {
    total: list.length,
    active: activeList.length,
    errorCount: errorList.length,
    status,
    connections: list,
  }
}
