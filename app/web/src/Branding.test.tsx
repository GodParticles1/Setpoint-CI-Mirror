// @vitest-environment jsdom

import { cleanup, render } from '@testing-library/react'
// @ts-expect-error Vitest runs this source assertion under Node; the product tsconfig intentionally excludes Node types.
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it } from 'vitest'
import { App } from './App'

afterEach(() => {
  cleanup()
  window.history.replaceState({}, '', '/')
})

describe('Setpoint canonical branding', () => {
  it('declares the canonical mark as the browser favicon', () => {
    const html = readFileSync('index.html', 'utf8')
    expect(html).toContain('<link rel="icon" href="/setpoint-mark.svg" type="image/svg+xml" />')
  })

  it('uses the same canonical mark in sidebar and mobile branding', () => {
    window.history.replaceState({}, '', '/runs/%E0%A4%A')
    const { container } = render(<App />)
    expect(container.querySelector('.brand img')?.getAttribute('src')).toBe('/setpoint-mark.svg')
    expect(container.querySelector('.mobile-brand img')?.getAttribute('src')).toBe('/setpoint-mark.svg')
  })

  it('does not keep an independent Activity icon as the application logo', () => {
    const source = readFileSync('src/App.tsx', 'utf8')
    expect(source).not.toMatch(/\bActivity\b/)
    expect(source).toContain('src="/setpoint-mark.svg"')
  })
})
