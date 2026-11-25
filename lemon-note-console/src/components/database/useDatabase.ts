import { ref, computed, reactive, onBeforeUnmount } from 'vue'
import { dbApi, type DbSchema, type DbView, type QueryResult, type DbRow, type ViewConfig, type DbProperty } from '@/lib/db'
import { realtime } from '@/lib/realtime'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { seedUsers } from '@/lib/users'

/** 一个数据库实例的状态与操作。 */
export function useDatabase(dbId: string, initialView?: string) {
  const toast = useToast()
  const page = ref<any>(null)
  const schema = ref<DbSchema>({ properties: [] })
  const views = ref<DbView[]>([])
  const role = ref('')
  const viewId = ref(initialView || '')
  const result = ref<QueryResult>({ rows: [], total: 0 })
  const loading = ref(true)
  const error = ref('')
  const search = ref('')
  const temp = reactive<{ filter?: any }>({})

  const view = computed(() => views.value.find((v) => v.id === viewId.value) || views.value[0])
  const props = computed(() => schema.value.properties)
  const prop = (id: string) => props.value.find((p) => p.id === id)
  const canEdit = computed(() => ['editor', 'admin', 'owner'].includes(role.value))
  const visibleProps = computed<DbProperty[]>(() => {
    const v = view.value
    const title = props.value.find((p) => p.type === 'title')!
    if (!v?.config.visible?.length) return props.value
    const list = v.config.visible.map((id) => prop(id)).filter(Boolean) as DbProperty[]
    return title && !list.includes(title) ? [title, ...list] : list
  })

  async function load() {
    try {
      const r = await dbApi.info(dbId)
      page.value = r.page
      schema.value = r.schema
      views.value = r.views
      role.value = r.role
      if (!views.value.some((v) => v.id === viewId.value)) viewId.value = views.value[0]?.id || ''
      await query()
    } catch (e) { error.value = errorText(e) } finally { loading.value = false }
  }

  let qseq = 0
  async function query() {
    const my = ++qseq
    try {
      const r = await dbApi.query(dbId, { view_id: viewId.value, search: search.value, filter: temp.filter })
      if (my === qseq) { seedUsers((r as any).users); result.value = r }
    } catch (e) { error.value = errorText(e) }
  }

  let timer: number | undefined
  function requery(delay = 250) {
    clearTimeout(timer)
    timer = window.setTimeout(query, delay)
  }

  async function addRow(props: Record<string, any> = {}, title = '', opts: { after_id?: string; first?: boolean; template_id?: string } = {}) {
    try {
      const r = await dbApi.addRow(dbId, { title, props, ...opts })
      await query()
      return r.row
    } catch (e) { toast.error(errorText(e)) }
  }

  async function setProp(row: DbRow, pid: string, value: any) {
    const p = prop(pid)
    try {
      if (p?.type === 'title') {
        await dbApi.patchRow(row.id, { props: { title: value } })
        row.title = value
      } else {
        const r = await dbApi.patchRow(row.id, { props: { [pid]: value } })
        if (r.row) Object.assign(row, r.row)
      }
      requery(400)
    } catch (e) { toast.error(errorText(e)); requery(0) }
  }

  async function moveRow(row: DbRow, afterId: string | null, extraProps?: Record<string, any>) {
    try {
      await dbApi.patchRow(row.id, { after_id: afterId ?? '', first: afterId === null, props: extraProps || undefined })
      await query()
    } catch (e) { toast.error(errorText(e)) }
  }

  async function saveSchema(next: DbSchema) {
    try {
      const r = await dbApi.saveSchema(dbId, next)
      schema.value = r.schema
      await query()
      return true
    } catch (e) { toast.error(errorText(e)); return false }
  }

  async function updateView(patch: Partial<ViewConfig>, extra: { name?: string; type?: string } = {}) {
    const v = view.value
    if (!v) return
    const config = { ...v.config, ...patch }
    v.config = config
    if (extra.name) v.name = extra.name
    if (extra.type) v.type = extra.type
    requery(0)
    if (!canEdit.value) return
    try { await dbApi.patchView(v.id, { config, ...extra }) } catch (e) { toast.error(errorText(e)) }
  }

  async function addView(type: string, name: string) {
    try {
      const cfg: ViewConfig = {}
      if (type === 'board') {
        const g = props.value.find((p) => p.type === 'status') || props.value.find((p) => p.type === 'select')
        if (g) cfg.group_by = { prop: g.id }
      }
      if (type === 'calendar' || type === 'timeline') {
        const d = props.value.find((p) => p.type === 'date')
        if (d) cfg.date_prop = d.id
      }
      const r = await dbApi.addView(dbId, { name, type, config: cfg })
      views.value.push(r.view)
      viewId.value = r.view.id
      await query()
    } catch (e) { toast.error(errorText(e)) }
  }

  async function deleteView(id: string) {
    try {
      await dbApi.delView(id)
      views.value = views.value.filter((v) => v.id !== id)
      if (viewId.value === id) viewId.value = views.value[0]?.id || ''
      await query()
    } catch (e) { toast.error(errorText(e)) }
  }

  const offPage = realtime.on('page', ({ meta }: any) => { if (meta.database_id === dbId || meta.id === dbId) requery(600) })
  const offDb = realtime.on('db', ({ id }: any) => { if (id === dbId) load() })
  onBeforeUnmount(() => { offPage(); offDb(); clearTimeout(timer) })

  return { page, schema, views, role, viewId, view, result, loading, error, search, temp, props, visibleProps, prop, canEdit,
    load, query, requery, addRow, setProp, moveRow, saveSchema, updateView, addView, deleteView }
}

export type DB = ReturnType<typeof useDatabase>
