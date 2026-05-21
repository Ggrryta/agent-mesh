import { Bot } from 'lucide-react'

const colors = [
  'from-blue-500 to-indigo-600',
  'from-emerald-500 to-teal-600',
  'from-violet-500 to-purple-600',
  'from-rose-500 to-pink-600',
  'from-amber-500 to-orange-600',
  'from-cyan-500 to-sky-600',
  'from-fuchsia-500 to-pink-600',
  'from-lime-500 to-emerald-600',
]

function hashColor(name: string): string {
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = (hash << 5) - hash + name.charCodeAt(i)
  return colors[Math.abs(hash) % colors.length]
}

interface Props {
  name: string
  status?: string
  size?: 'sm' | 'md' | 'lg'
  showStatus?: boolean
}

export default function AgentAvatar({ name, status, size = 'md', showStatus = false }: Props) {
  const sizeClass = size === 'sm' ? 'h-8 w-8' : size === 'lg' ? 'h-14 w-14' : 'h-10 w-10'
  const iconSize = size === 'sm' ? 'h-4 w-4' : size === 'lg' ? 'h-7 w-7' : 'h-5 w-5'
  const statusDot = size === 'sm' ? 'h-2 w-2' : 'h-2.5 w-2.5'
  const statusColor =
    status === 'active' ? 'bg-emerald-500' :
    status === 'draining' ? 'bg-amber-500' :
    status === 'inactive' ? 'bg-gray-400' : 'bg-gray-300'

  return (
    <div className="relative inline-block shrink-0">
      <div className={`${sizeClass} rounded-xl bg-gradient-to-br ${hashColor(name)} flex items-center justify-center shadow-sm`}>
        <Bot className={`${iconSize} text-white`} />
      </div>
      {showStatus && (
        <div className={`absolute -bottom-0.5 -right-0.5 ${statusDot} rounded-full ${statusColor} ring-2 ring-white`} />
      )}
    </div>
  )
}
