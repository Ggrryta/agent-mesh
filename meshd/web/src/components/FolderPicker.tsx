// FolderPicker.tsx — 文件夹选择弹窗，通过 meshd /api/fs/browse 浏览本机目录
import { useState, useEffect } from 'react'
import { meshdApi } from '../api/client'

interface Props {
  open: boolean
  initial?: string
  onSelect: (path: string) => void
  onClose: () => void
}

interface BrowseResult {
  current: string
  parent: string | null
  directories: { name: string; path: string }[]
  error?: string
}

export default function FolderPicker({ open, initial, onSelect, onClose }: Props) {
  const [data, setData] = useState<BrowseResult | null>(null)
  const [loading, setLoading] = useState(false)

  const browse = async (path?: string) => {
    setLoading(true)
    try {
      const params = path ? `?path=${encodeURIComponent(path)}` : ''
      const res = await meshdApi.get<BrowseResult>(`/fs/browse${params}`)
      setData(res.data)
    } catch {
      // ignore
    }
    setLoading(false)
  }

  useEffect(() => {
    if (open) browse(initial || undefined)
  }, [open])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onClick={onClose}>
      <div className="bg-background rounded-xl shadow-xl w-[500px] max-h-[70vh] flex flex-col" onClick={e => e.stopPropagation()}>
        {/* Header */}
        <div className="px-4 py-3 border-b flex items-center justify-between">
          <h3 className="text-sm font-semibold">选择工作目录</h3>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground text-lg">×</button>
        </div>

        {/* Current path */}
        <div className="px-4 py-2 bg-muted/30 border-b">
          <code className="text-xs text-muted-foreground break-all">{data?.current || '...'}</code>
        </div>

        {/* Directory list */}
        <div className="flex-1 overflow-y-auto px-2 py-2 min-h-[200px]">
          {loading && <p className="text-sm text-muted-foreground px-2 py-4">加载中...</p>}
          {data?.error && <p className="text-sm text-red-500 px-2 py-2">{data.error}</p>}

          {/* Parent directory */}
          {data?.parent && (
            <button
              onClick={() => browse(data.parent!)}
              className="w-full text-left px-3 py-2 rounded-md hover:bg-muted text-sm flex items-center gap-2"
            >
              <span className="text-muted-foreground">📁</span>
              <span className="text-muted-foreground">..</span>
            </button>
          )}

          {/* Subdirectories */}
          {data?.directories.map(d => (
            <button
              key={d.path}
              onClick={() => browse(d.path)}
              className="w-full text-left px-3 py-2 rounded-md hover:bg-muted text-sm flex items-center gap-2"
            >
              <span>📁</span>
              <span>{d.name}</span>
            </button>
          ))}

          {data && !data.error && data.directories.length === 0 && (
            <p className="text-sm text-muted-foreground px-2 py-4">此目录下没有子文件夹</p>
          )}
        </div>

        {/* Footer */}
        <div className="px-4 py-3 border-t flex items-center justify-between">
          <span className="text-xs text-muted-foreground truncate max-w-[300px]">{data?.current}</span>
          <div className="flex gap-2">
            <button onClick={onClose} className="px-3 py-1.5 text-sm rounded-md border hover:bg-muted">取消</button>
            <button
              onClick={() => { if (data?.current) onSelect(data.current); onClose() }}
              className="px-3 py-1.5 text-sm rounded-md bg-foreground text-background hover:bg-foreground/90"
            >
              选择此目录
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
