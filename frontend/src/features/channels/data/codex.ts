import { apiRequest } from '@/lib/api-client'
import type { ProxyConfig } from '../hooks/use-oauth-flow'

export interface CodexDeviceStartResult {
  session_id: string
  user_code: string
  verification_uri: string
  expires_in: number
  interval: number
}

export interface CodexDeviceStartInput {
  proxy?: ProxyConfig
}

export interface CodexDevicePollInput {
  session_id: string
  proxy?: ProxyConfig
}

export interface CodexDevicePollResult {
  status: 'pending' | 'success' | string
  credentials?: string
  message?: string
}

export async function codexOAuthStart(headers?: Record<string, string>): Promise<{ session_id: string; auth_url: string }> {
  return apiRequest('/admin/codex/oauth/start', {
    method: 'POST',
    body: {},
    headers,
    requireAuth: true,
  })
}

export async function codexDeviceStart(
  input: CodexDeviceStartInput = {},
  headers?: Record<string, string>
): Promise<CodexDeviceStartResult> {
  return apiRequest('/admin/codex/device/start', {
    method: 'POST',
    body: input,
    headers,
    requireAuth: true,
  })
}

export async function codexDevicePoll(
  input: CodexDevicePollInput,
  headers?: Record<string, string>
): Promise<CodexDevicePollResult> {
  return apiRequest('/admin/codex/device/poll', {
    method: 'POST',
    body: input,
    headers,
    requireAuth: true,
  })
}

export async function codexOAuthExchange(
  input: {
    session_id: string
    callback_url: string
    proxy?: ProxyConfig
  },
  headers?: Record<string, string>
): Promise<{ credentials: string }> {
  return apiRequest('/admin/codex/oauth/exchange', {
    method: 'POST',
    body: input,
    headers,
    requireAuth: true,
  })
}

export async function codexDecodeAuthJSON(
  input: {
    auth_json: string
  },
  headers?: Record<string, string>
): Promise<{ credentials: string }> {
  return apiRequest('/admin/codex/auth/decode', {
    method: 'POST',
    body: input,
    headers,
    requireAuth: true,
  })
}
