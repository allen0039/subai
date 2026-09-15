import { describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const messages: Record<string, string> = {
        'admin.accounts.oauth.openai.failedToExchangeCode': 'OpenAI 授权码兑换失败',
        'admin.accounts.oauth.openai.errors.OPENAI_OAUTH_PROXY_REQUIRED':
          '未设置代理，当前服务器无法直连 OpenAI，导致 OpenAI OAuth 请求失败。请先选择可访问 OpenAI 的代理后重试；如果授权码已失效，请重新生成授权链接。'
      }
      return messages[key] ?? key
    }
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      startSubAIOAuthSession: vi.fn(),
      completeSubAIOAuthSession: vi.fn()
    }
  }
}))

import { useOpenAIOAuth } from '@/composables/useOpenAIOAuth'
import { adminAPI } from '@/api/admin'

describe('useOpenAIOAuth.exchangeAuthCode', () => {
  it('shows a clear proxy hint when code exchange fails without a proxy', async () => {
    vi.mocked(adminAPI.accounts.completeSubAIOAuthSession).mockRejectedValueOnce({
      status: 502,
      reason: 'OPENAI_OAUTH_PROXY_REQUIRED',
      message: 'OpenAI OAuth token exchange failed: no proxy is configured.'
    })
    const oauth = useOpenAIOAuth()

    const tokenInfo = await oauth.exchangeAuthCode('http://localhost:1455/auth/callback?code=code&state=state', 'session-id', 'state')

    expect(tokenInfo).toBeNull()
    expect(oauth.error.value).toBe(
      '未设置代理，当前服务器无法直连 OpenAI，导致 OpenAI OAuth 请求失败。请先选择可访问 OpenAI 的代理后重试；如果授权码已失效，请重新生成授权链接。'
    )
  })
})

describe('SubAI callback URL contract', () => {
  it('sends the actual full callback without substituting cached state', async () => {
    vi.mocked(adminAPI.accounts.completeSubAIOAuthSession).mockReset().mockResolvedValueOnce({
      ok: true,
      account_id: 42,
      quota_synced: true
    })
    const oauth = useOpenAIOAuth()
    const callback = 'http://localhost:1455/auth/callback?code=c&state=actual-state'
    await oauth.exchangeAuthCode(callback, 'sid', 'cached-state')
    expect(adminAPI.accounts.completeSubAIOAuthSession).toHaveBeenCalledWith('sid', callback)
  })

  it('rejects a code without the complete callback URL', async () => {
    vi.mocked(adminAPI.accounts.completeSubAIOAuthSession).mockReset()
    const oauth = useOpenAIOAuth()
    expect(await oauth.exchangeAuthCode('bare-code', 'sid', 'cached-state')).toBeNull()
    expect(adminAPI.accounts.completeSubAIOAuthSession).not.toHaveBeenCalled()
  })
})

describe('SubAI authorization session start', () => {
  it('uses the original session endpoint contract for a new account', async () => {
    vi.mocked(adminAPI.accounts.startSubAIOAuthSession).mockResolvedValueOnce({
      id: 'sid-new',
      state: 'state-new',
      authorize_url: 'https://auth.openai.com/oauth/authorize?state=state-new'
    })
    const oauth = useOpenAIOAuth()

    expect(await oauth.generateAuthUrl(9)).toBe(true)
    expect(adminAPI.accounts.startSubAIOAuthSession).toHaveBeenCalledWith({ proxy_id: 9 })
    expect(oauth.sessionId.value).toBe('sid-new')
    expect(oauth.oauthState.value).toBe('state-new')
  })

  it('binds reauthorization to the existing account', async () => {
    vi.mocked(adminAPI.accounts.startSubAIOAuthSession).mockResolvedValueOnce({
      id: 'sid-reuse',
      state: 'state-reuse',
      authorize_url: 'https://auth.openai.com/oauth/authorize?state=state-reuse'
    })
    const oauth = useOpenAIOAuth()

    expect(await oauth.generateAuthUrl(9, 42)).toBe(true)
    expect(adminAPI.accounts.startSubAIOAuthSession).toHaveBeenCalledWith({
      proxy_id: 9,
      reuse_account_id: '42'
    })
  })
})
