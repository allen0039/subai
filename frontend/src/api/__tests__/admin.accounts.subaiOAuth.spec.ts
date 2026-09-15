import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    defaults: { baseURL: '/api/v1' },
    get,
    post,
  },
}))

import {
  completeSubAIOAuthSession,
  getSubAIOAuthSession,
  startSubAIOAuthSession,
} from '@/api/admin/accounts'

describe('original SubAI GPT OAuth API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('starts a new single-use session outside the Sub2API /api/v1 routes', async () => {
    post.mockResolvedValueOnce({
      data: {
        id: 'sid',
        state: 'state',
        authorize_url: 'https://auth.openai.com/oauth/authorize',
      },
    })

    await startSubAIOAuthSession({ proxy_id: 9 })

    expect(post).toHaveBeenCalledWith(
      `${window.location.origin}/api/admin/accounts/oauth/sessions`,
      { proxy_id: 9 },
    )
  })

  it('uses the same session for status and callback completion', async () => {
    get.mockResolvedValueOnce({ data: { id: 'sid/encoded', status: 'pending' } })
    post.mockResolvedValueOnce({
      data: { ok: true, account_id: 42, quota_synced: true },
    })

    await getSubAIOAuthSession('sid/encoded')
    await completeSubAIOAuthSession(
      'sid/encoded',
      'http://localhost:1455/auth/callback?code=code&state=state',
    )

    const sessionURL = `${window.location.origin}/api/admin/accounts/oauth/sessions/sid%2Fencoded`
    expect(get).toHaveBeenCalledWith(sessionURL)
    expect(post).toHaveBeenCalledWith(`${sessionURL}/callback`, {
      callback_url: 'http://localhost:1455/auth/callback?code=code&state=state',
    })
  })
})
