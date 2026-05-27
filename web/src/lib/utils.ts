import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function shortToken(token: string) {
  if (token.length <= 28) return token || 'No token found'
  return `${token.slice(0, 18)}...${token.slice(-12)}`
}

export function maskToken(token: string) {
  if (!token) return ''
  return `${token.slice(0, 12)}${'*'.repeat(24)}${token.slice(-10)}`
}

export function titleCase(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

export function sumCounts(counts: Record<string, number>) {
  return Object.values(counts).reduce((total, count) => total + count, 0)
}
