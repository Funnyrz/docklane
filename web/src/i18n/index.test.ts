import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, APIError, message } from '../api'
import {
  detectLocale, enMessages, formatDateTime, formatNumber, i18n, initializeLocale,
  locale, localeStorageKey, localized, renderText, setLocale, t, zhMessages,
} from './index'

beforeEach(() => { localStorage.clear(); setLocale('zh-CN', false) })
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); localStorage.clear() })

describe('language detection and persistence', () => {
  it.each([
    [['zh-TW'], undefined, 'zh-CN'], [['ZH-hk'], undefined, 'zh-CN'],
    [['en-GB'], undefined, 'en'], [['EN'], undefined, 'en'],
    [['fr-FR', 'en-AU', 'zh-CN'], undefined, 'en'],
    [['fr-FR', 'zh-Hans', 'en-US'], undefined, 'zh-CN'],
    [['ja-JP'], undefined, 'zh-CN'], [[], undefined, 'zh-CN'],
    [['en-US'], 'zh-CN', 'zh-CN'], [['zh-CN'], 'en', 'en'],
    [['en-US'], 'invalid', 'en'], [['en-US'], 'zh-TW', 'en'],
    [['zh-CN'], 'en-US', 'zh-CN'],
  ] as const)('matches ordered languages %j with saved value %s', (languages, saved, expected) => {
    expect(detectLocale(languages, saved)).toBe(expected)
  })

  it('restores a saved choice before considering browser languages without writing it again', () => {
    localStorage.setItem(localeStorageKey, 'en')
    vi.spyOn(navigator, 'languages', 'get').mockReturnValue(['zh-CN'])
    const write = vi.spyOn(Storage.prototype, 'setItem')
    initializeLocale()
    expect(locale.value).toBe('en'); expect(write).not.toHaveBeenCalled()
  })

  it('uses the browser language when storage contains an invalid value', () => {
    localStorage.setItem(localeStorageKey, 'fr')
    vi.spyOn(navigator, 'languages', 'get').mockReturnValue(['fr-FR', 'en-GB'])
    initializeLocale()
    expect(locale.value).toBe('en')
  })

  it('falls back to navigator.language when the languages list is empty', () => {
    vi.spyOn(navigator, 'languages', 'get').mockReturnValue([])
    vi.spyOn(navigator, 'language', 'get').mockReturnValue('en-AU')
    initializeLocale()
    expect(locale.value).toBe('en')
  })

  it('detects a language even when reading storage is denied', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new DOMException('denied', 'SecurityError') })
    vi.spyOn(navigator, 'languages', 'get').mockReturnValue(['en-US'])
    expect(() => initializeLocale()).not.toThrow()
    expect(locale.value).toBe('en')
  })

  it('persists manual choices and updates document language and title', () => {
    setLocale('en')
    expect(localStorage.getItem(localeStorageKey)).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    expect(document.title).toBe(enMessages.common.documentTitle)
    setLocale('zh-CN')
    expect(localStorage.getItem(localeStorageKey)).toBe('zh-CN')
    expect(document.documentElement.lang).toBe('zh-CN')
    expect(document.title).toBe(zhMessages.common.documentTitle)
  })

  it('still switches language when writing storage is denied', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('full', 'QuotaExceededError') })
    expect(() => setLocale('en')).not.toThrow()
    expect(locale.value).toBe('en'); expect(t('common.cancel')).toBe('Cancel')
    expect(document.documentElement.lang).toBe('en')
  })
})

function leaves(value: object, prefix = ''): Record<string, string> {
  return Object.fromEntries(Object.entries(value).flatMap(([key, item]) => {
    const path = prefix ? `${prefix}.${key}` : key
    return typeof item === 'string' ? [[path, item]] : Object.entries(leaves(item as object, path))
  }))
}

describe('bilingual message catalog', () => {
  it('has matching keys and named interpolation parameters', () => {
    const zh = leaves(zhMessages), en = leaves(enMessages)
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
    for (const key of Object.keys(zh)) {
      const params = (text: string) => [...text.matchAll(/\{([a-zA-Z]\w*)\}/g)].map(match => match[1]).sort()
      expect(params(en[key]!), key).toEqual(params(zh[key]!))
    }
  })

  it('compiles and renders every message in both languages without syntax errors', () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (const language of ['zh-CN', 'en'] as const) {
      setLocale(language, false)
      for (const [key, text] of Object.entries(leaves(language === 'en' ? enMessages : zhMessages))) {
        const params = Object.fromEntries([...text.matchAll(/\{([a-zA-Z]\w*)\}/g)].map(match => [match[1], 'sample']))
        expect(typeof i18n.global.t(key, params), `${language}:${key}`).toBe('string')
      }
    }
    expect(error.mock.calls).toEqual([])
    expect(warn.mock.calls).toEqual([])
  })

  it('renders stored text descriptors using the current language', () => {
    const descriptor = localized('common.required', { field: 'fixture' })
    expect(renderText(descriptor)).toBe('请填写fixture')
    setLocale('en', false)
    expect(renderText(descriptor)).toBe('Please enter fixture')
  })

  it('formats numbers and dates with the current locale while preserving an explicit timezone', () => {
    const raw = '2026-10-17T00:15:00Z', options: Intl.DateTimeFormatOptions = { timeZone: 'UTC', dateStyle: 'long', timeStyle: 'short' }
    expect(formatDateTime(raw, options)).toBe(new Intl.DateTimeFormat('zh-CN', options).format(new Date(raw)))
    setLocale('en', false)
    expect(formatDateTime(raw, options)).toBe(new Intl.DateTimeFormat('en-US', options).format(new Date(raw)))
    expect(formatNumber(12345.6)).toBe(new Intl.NumberFormat('en-US').format(12345.6))
    expect(formatDateTime('invalid')).toBe('—'); expect(formatDateTime(null)).toBe('—')
  })
})

describe('API error presentation', () => {
  it('preserves status, code and raw message while translating a known failure at display time', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'ENGINE_OFFLINE', message: 'dial unix /var/run/docker.sock: connection refused' } }), { status: 503 })))
    let failure: unknown
    try { await api('/hosts/local/dashboard') } catch (error) { failure = error }
    expect(failure).toBeInstanceOf(APIError)
    expect(failure).toMatchObject({ status: 503, code: 'ENGINE_OFFLINE', message: 'dial unix /var/run/docker.sock: connection refused' })
    expect(message(failure)).toContain(zhMessages.errors.ENGINE_OFFLINE)
    setLocale('en', false)
    expect(message(failure)).toContain(enMessages.errors.ENGINE_OFFLINE)
    expect(message(failure)).toContain('dial unix /var/run/docker.sock: connection refused')
    expect(failure).toMatchObject({ status: 503, code: 'ENGINE_OFFLINE', message: 'dial unix /var/run/docker.sock: connection refused' })
  })

  it('keeps unknown server errors and ordinary exceptions readable in either language', () => {
    const failure = new APIError(418, 'CUSTOM_UPSTREAM_ERROR', 'upstream supplied this exact detail')
    for (const language of ['zh-CN', 'en'] as const) {
      setLocale(language, false)
      expect(message(failure)).toBe('upstream supplied this exact detail')
      expect(message(new Error('raw network detail'))).toBe('raw network detail')
    }
    expect(failure).toMatchObject({ status: 418, code: 'CUSTOM_UPSTREAM_ERROR', message: 'upstream supplied this exact detail' })
  })
})
