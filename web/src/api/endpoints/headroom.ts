import type { HeadroomExtrasResponse, HeadroomStatusResponse } from '../types'
import { request } from '../runtime'

const headroomEndpoints = {
  // Headroom
  getHeadroomStatus: () =>
    request<HeadroomStatusResponse>('/api/headroom/status'),
  getHeadroomExtras: (log = false) =>
    request<HeadroomExtrasResponse>(`/api/headroom/extras${log ? '?log=1' : ''}`),
  startHeadroom: () =>
    request<{ success?: boolean; pid?: number; error?: string }>('/api/headroom/start', {
      method: 'POST',
    }),
  stopHeadroom: () =>
    request<{ success?: boolean; error?: string }>('/api/headroom/stop', {
      method: 'POST',
    }),
  restartHeadroom: () =>
    request<{ success?: boolean; pid?: number; error?: string }>('/api/headroom/restart', {
      method: 'POST',
    }),
  installHeadroomExtras: (extras: string[]) =>
    request<HeadroomExtrasResponse>('/api/headroom/extras', {
      method: 'POST',
      body: JSON.stringify({ extras }),
    }),
  uninstallHeadroomExtras: (extras: string[]) =>
    request<HeadroomExtrasResponse>('/api/headroom/extras', {
      method: 'DELETE',
      body: JSON.stringify({ extras }),
    }),
}

export default headroomEndpoints
