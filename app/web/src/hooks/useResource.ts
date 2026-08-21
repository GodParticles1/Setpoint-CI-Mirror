import { useCallback, useEffect, useState } from 'react'

export function useResource<T>(loader: (signal: AbortSignal) => Promise<T>, dependencies: readonly unknown[] = []) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)
  const [settledRevision, setSettledRevision] = useState(0)
  const [failureCount, setFailureCount] = useState(0)
  const refresh = useCallback(() => setRevision((value) => value + 1), [])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    loader(controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return
        setData(value)
        setError('')
        setFailureCount(0)
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return
        setError(reason instanceof Error ? reason.message : '读取失败')
        setFailureCount((value) => value + 1)
      })
      .finally(() => {
        if (controller.signal.aborted) return
        setLoading(false)
        setSettledRevision((value) => value + 1)
      })
    return () => controller.abort()
  }, [...dependencies, revision])

  return { data, error, loading, failureCount, settledRevision, refresh, setData }
}
