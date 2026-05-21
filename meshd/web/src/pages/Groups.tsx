import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Plus, Users, Sparkles, Clock } from 'lucide-react'
import api from '../api/client'
import AgentAvatar from '../components/AgentAvatar'

interface GroupSummary {
  group_id: string
  context_id: string
  name: string
  member_count?: number
  skills?: string[]
  members?: string[]
}

export default function Groups() {
  const [groups, setGroups] = useState<GroupSummary[]>([])
  const [newGroupId, setNewGroupId] = useState('')
  const [newGroupName, setNewGroupName] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const navigate = useNavigate()

  useEffect(() => {
    loadGroups()
  }, [])

  const loadGroups = async () => {
    try {
      const res = await api.get('/groups')
      setGroups(res.data.groups || [])
    } catch {}
  }

  const createGroup = async () => {
    if (!newGroupId) return
    try {
      await api.post('/groups', { group_id: newGroupId, name: newGroupName || newGroupId })
      navigate(`/groups/${newGroupId}`)
    } catch {}
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-6xl mx-auto">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <button onClick={() => navigate('/')} className="p-2 rounded-lg hover:bg-white border border-transparent hover:border-border transition-all">
              <ArrowLeft className="h-4 w-4 text-gray-500" />
            </button>
            <div>
              <h2 className="text-xl font-bold text-foreground">Teams</h2>
              <p className="text-sm text-muted-foreground">Collaborative agent groups with complementary skills</p>
            </div>
          </div>
          <button
            onClick={() => setShowCreate(!showCreate)}
            className="flex items-center gap-2 px-4 py-2.5 rounded-lg bg-gradient-to-r from-violet-600 to-purple-600 text-white text-sm font-medium shadow-sm shadow-violet-200 hover:shadow-md transition-all"
          >
            <Plus className="h-4 w-4" />
            New Team
          </button>
        </div>

        {/* Create form */}
        {showCreate && (
          <div className="mb-6 p-5 rounded-xl border border-border bg-white shadow-sm">
            <h3 className="text-sm font-semibold text-foreground mb-3">Create Team</h3>
            <div className="flex gap-3">
              <input
                placeholder="Team ID (e.g. content-team)"
                value={newGroupId}
                onChange={(e) => setNewGroupId(e.target.value)}
                className="flex-1 px-3 py-2.5 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              />
              <input
                placeholder="Display name"
                value={newGroupName}
                onChange={(e) => setNewGroupName(e.target.value)}
                className="flex-1 px-3 py-2.5 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              />
              <button
                onClick={createGroup}
                disabled={!newGroupId}
                className="px-5 py-2.5 rounded-lg bg-gray-900 text-white text-sm font-medium hover:bg-gray-800 disabled:opacity-50 transition-colors"
              >
                Create
              </button>
            </div>
          </div>
        )}

        {/* Group grid */}
        {groups.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border bg-white/50 p-12 text-center">
            <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-violet-100 to-purple-100 flex items-center justify-center mx-auto mb-4">
              <Users className="h-8 w-8 text-violet-600" />
            </div>
            <h3 className="text-lg font-semibold text-foreground mb-2">No teams yet</h3>
            <p className="text-sm text-muted-foreground mb-4 max-w-md mx-auto">
              Create a team of agents with complementary skills. They can collaborate autonomously to complete complex tasks.
            </p>
            <button
              onClick={() => setShowCreate(true)}
              className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-violet-50 text-violet-700 text-sm font-medium hover:bg-violet-100 transition-colors"
            >
              <Plus className="h-4 w-4" />
              Create your first team
            </button>
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {groups.map(g => <GroupCard key={g.group_id} group={g} onClick={() => navigate(`/groups/${g.group_id}`)} />)}
          </div>
        )}
      </div>
    </div>
  )
}

function GroupCard({ group, onClick }: { group: GroupSummary; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className="group text-left p-5 rounded-xl border border-border bg-white shadow-sm hover:shadow-md hover:-translate-y-0.5 transition-all"
    >
      {/* Header */}
      <div className="flex items-start justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className="w-11 h-11 rounded-xl bg-gradient-to-br from-violet-500 to-purple-600 flex items-center justify-center shadow-sm shadow-violet-200">
            <Users className="h-5 w-5 text-white" />
          </div>
          <div>
            <h3 className="text-base font-semibold text-foreground group-hover:text-violet-600 transition-colors">{group.name}</h3>
            <p className="text-xs text-muted-foreground font-mono">{group.group_id}</p>
          </div>
        </div>
      </div>

      {/* Member avatars stacked */}
      {group.members && group.members.length > 0 && (
        <div className="flex items-center gap-2 mb-4">
          <div className="flex -space-x-2">
            {group.members.slice(0, 5).map((m, i) => (
              <div key={m} className="ring-2 ring-white rounded-xl" style={{ zIndex: 10 - i }}>
                <AgentAvatar name={m} size="sm" />
              </div>
            ))}
            {group.members.length > 5 && (
              <div className="h-8 w-8 rounded-xl bg-gray-100 ring-2 ring-white flex items-center justify-center text-xs font-medium text-gray-600">
                +{group.members.length - 5}
              </div>
            )}
          </div>
          <span className="text-xs text-muted-foreground ml-1">{group.members.length} members</span>
        </div>
      )}

      {/* Skills cloud */}
      {group.skills && group.skills.length > 0 && (
        <div className="flex items-start gap-1.5 mb-3">
          <Sparkles className="h-3.5 w-3.5 text-amber-500 shrink-0 mt-0.5" />
          <div className="flex flex-wrap gap-1">
            {group.skills.slice(0, 5).map(s => (
              <span key={s} className="px-2 py-0.5 rounded-md bg-gray-100 text-xs font-medium text-gray-700">{s}</span>
            ))}
            {group.skills.length > 5 && (
              <span className="px-2 py-0.5 rounded-md text-xs text-muted-foreground">+{group.skills.length - 5}</span>
            )}
          </div>
        </div>
      )}

      {/* Footer */}
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground pt-3 border-t border-border">
        <Clock className="h-3 w-3" />
        Recently active
      </div>
    </button>
  )
}
