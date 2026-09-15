import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import type { SubAIOAuthCompletion } from '@/api/admin/accounts'

export type OpenAIOAuthPlatform = 'openai'

export function useOpenAIOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  // State
  const authUrl = ref('')
  const sessionId = ref('')
  const oauthState = ref('')
  const loading = ref(false)
  const error = ref('')

  // Reset state
  const resetState = () => {
    authUrl.value = ''
    sessionId.value = ''
    oauthState.value = ''
    loading.value = false
    error.value = ''
  }

  // Generate auth URL for OpenAI OAuth
  const generateAuthUrl = async (
    proxyId?: number | null,
    reuseAccountId?: number | null
  ): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    oauthState.value = ''
    error.value = ''

    try {
      const payload: { proxy_id?: number; reuse_account_id?: string } = {}
      if (proxyId) {
        payload.proxy_id = proxyId
      }
      if (reuseAccountId) {
        payload.reuse_account_id = String(reuseAccountId)
      }

      const response = await adminAPI.accounts.startSubAIOAuthSession(payload)
      authUrl.value = response.authorize_url || ''
      sessionId.value = response.id
      oauthState.value = response.state || ''
      try {
        if (!oauthState.value) {
          const parsed = new URL(response.authorize_url || '')
          oauthState.value = parsed.searchParams.get('state') || ''
        }
      } catch {
        // The backend-provided state remains authoritative when the URL cannot be parsed.
      }
      return true
    } catch (err: any) {
      error.value = extractApiErrorMessage(err, t('admin.accounts.oauth.openai.failedToGenerateUrl'))
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  // Exchange auth code for tokens
  const exchangeAuthCode = async (
    code: string,
    currentSessionId: string,
    _state: string,
    _proxyId?: number | null
  ): Promise<SubAIOAuthCompletion | null> => {
    if (!code.trim() || !currentSessionId) {
      error.value = t('admin.accounts.oauth.openai.callbackUrlRequired')
      return null
    }
    // Preserve the complete callback for server-side host/path/state checks.
    try {
      const callback = new URL(code.trim())
      if (!['http:', 'https:'].includes(callback.protocol) || !callback.searchParams.get('state') || !callback.searchParams.get('code')) throw new Error('invalid callback')
    } catch {
      error.value = t('admin.accounts.oauth.openai.callbackUrlRequired')
      appStore.showError(error.value)
      return null
    }

    loading.value = true
    error.value = ''

    try {
      return await adminAPI.accounts.completeSubAIOAuthSession(currentSessionId, code.trim())
    } catch (err: any) {
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.accounts.oauth.openai.errors',
        t('admin.accounts.oauth.openai.failedToExchangeCode')
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  return {
    // State
    authUrl,
    sessionId,
    oauthState,
    loading,
    error,
    // Methods
    resetState,
    generateAuthUrl,
    exchangeAuthCode
  }
}
