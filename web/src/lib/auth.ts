// React hooks and helpers around pb.authStore.
//
// pb.authStore is a vanilla event emitter; we wrap it in a React state hook
// so components re-render on login / logout / token refresh.

import { useEffect, useState } from 'react'
import { pb, AUTH_COLLECTION } from './pb'

export type AuthUser = {
  id: string
  email: string
  name?: string
  avatar?: string
  username?: string
}

export type AuthState = {
  isAuthed: boolean
  token: string
  user: AuthUser | null
}

function snapshot(): AuthState {
  const record = pb.authStore.record as Record<string, unknown> | null
  return {
    isAuthed: pb.authStore.isValid,
    token: pb.authStore.token,
    user: record
      ? {
          id: String(record.id ?? ''),
          email: String(record.email ?? ''),
          name: record.name ? String(record.name) : undefined,
          avatar: record.avatar ? String(record.avatar) : undefined,
          username: record.username ? String(record.username) : undefined,
        }
      : null,
  }
}

export function useAuth(): AuthState {
  const [state, setState] = useState<AuthState>(snapshot)
  useEffect(() => {
    return pb.authStore.onChange(() => setState(snapshot()))
  }, [])
  return state
}

// Trigger PocketBase's popup-based OAuth2 flow against the GitHub provider.
// PocketBase opens a popup at /api/oauth2-redirect, runs the OAuth dance,
// and stores token + record in pb.authStore via the SDK.
export async function loginWithGitHub() {
  return pb.collection(AUTH_COLLECTION).authWithOAuth2({ provider: 'github' })
}

export function logout() {
  pb.authStore.clear()
}
