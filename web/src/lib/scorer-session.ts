// A synchronous lock plus revision guard. Abort is best-effort (it cannot undo
// server-side quota use); identity checking also rejects uncooperative responses.
export class ScorerSession {
  private revision = 0
  private confirmedRevision: number | null = null
  private active: { revision: number; controller: AbortController } | null = null

  get confirmed() { return this.confirmedRevision === this.revision }
  get busy() { return this.active !== null }
  confirm() { if (!this.busy) this.confirmedRevision = this.revision }
  invalidate() {
    this.active?.controller.abort()
    this.active = null
    this.revision += 1
    this.confirmedRevision = null
  }
  begin() {
    if (this.busy) return null
    const request = { revision: this.revision, controller: new AbortController() }
    this.active = request
    return request
  }
  current(request: NonNullable<ScorerSession['active']>) {
    return this.active === request && request.revision === this.revision && !request.controller.signal.aborted
  }
  finish(request: NonNullable<ScorerSession['active']>) {
    if (this.current(request)) this.active = null
  }
}
