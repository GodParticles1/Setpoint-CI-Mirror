import { newIdempotencyKey } from './format'

export class IdempotentOperation {
  private fingerprint = ''
  private key = ''

  keyFor(fingerprint: string) {
    if (!this.key || this.fingerprint !== fingerprint) {
      this.fingerprint = fingerprint
      this.key = newIdempotencyKey()
    }
    return this.key
  }

  complete() {
    this.fingerprint = ''
    this.key = ''
  }
}
