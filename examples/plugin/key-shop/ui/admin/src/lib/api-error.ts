export class ApiError extends Error {
  readonly status: number
  readonly keyCount: number | null

  constructor(message: string, status: number, keyCount: number | null = null) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.keyCount = keyCount
  }
}
