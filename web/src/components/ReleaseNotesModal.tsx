import { useEffect, useState } from 'react'
import { Alert, Button, Modal, Select, Space, Spin, Tag, Typography } from 'antd'
import { ExportOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { buildKey, type BuildIdentity } from '../lib/buildIdentity'
import { productVersionLabel } from '../lib/productVersionLabel'
import type { UpdatePolicy } from '../lib/updateState'
import Markdown from './Markdown'

// The one place the portal shows release notes.
//
// It follows the Run Analysis dialog's conventions — same 980px desktop width, same mask, header and
// button treatment, same responsive rules (index.css) — because those are the portal's one idea of
// what a working dialog looks like, and a second, independently-styled one would read as a different
// product bolted on.
//
// It is also the surface a `required` policy escalates to. The reader can close it; the coordinator
// then keeps the update visible in the site-wide banner instead of trapping work behind an overlay.

type ReleaseMaturity = 'release' | 'beta'
export type ReleaseNotes = { tag: string; available: boolean; markdown: string; url: string; maturity: ReleaseMaturity | '' }
export type ReleaseHistoryItem = { tag: string; title: string; url: string; maturity: ReleaseMaturity }

type NotesState =
  | { status: 'loading' }
  | { status: 'loaded'; notes: ReleaseNotes }
  | { status: 'error' }

export default function ReleaseNotesModal({
  open,
  target,
  current,
  policy,
  refreshing,
  onClose,
  onRefresh,
}: {
  open: boolean
  /** The build whose notes to show — the update target, or the page itself when browsing. */
  target: BuildIdentity | null
  /** The build this page is running, named when it differs from the target. */
  current: BuildIdentity
  policy: UpdatePolicy
  refreshing: boolean
  onClose: () => void
  /** Omitted when there is nothing to switch to (browsing the current build). */
  onRefresh?: () => void
}) {
  const { t, i18n } = useTranslation()
  const [state, setState] = useState<NotesState>({ status: 'loading' })
  const [attempt, setAttempt] = useState(0)
  const [selectedTag, setSelectedTag] = useState(target?.version ?? '')
  const [history, setHistory] = useState<ReleaseHistoryItem[]>([])
  // The site policy may be `required` while the reader manually browses the current version from
  // the footer. Only an actual update handover has a refresh action and needs the unsaved-work
  // warning; ordinary history browsing must not inherit the site's reminder policy.
  const reminder = policy === 'required' && !!onRefresh
  const tag = target?.version ?? ''
  const targetKey = target ? buildKey(target) : ''
  const browseHistory = !onRefresh

  useEffect(() => {
    if (open) setSelectedTag(tag)
  }, [open, tag, targetKey])

  useEffect(() => {
    if (!open || !browseHistory) return
    let live = true
    api.get<{ items: ReleaseHistoryItem[] }>('/api/release-history')
      .then((result) => {
        if (live) setHistory(result.items ?? [])
      })
      .catch(() => {
        if (live) setHistory([])
      })
    return () => {
      live = false
    }
  }, [browseHistory, open])

  useEffect(() => {
    if (!open || !selectedTag) return
    let live = true
    setState({ status: 'loading' })
    // The tag is passed through the query string, not interpolated into a path: it is compared
    // against the server's own tag server-side, and a value that is not a release tag simply has no
    // note to serve.
    api
      .get<ReleaseNotes>(`/api/release-notes?tag=${encodeURIComponent(selectedTag)}`)
      // `live` is the whole guard: the target moving (a second deploy) changes a dependency, so this
      // effect is torn down and a slow answer for the previous target can never be painted under the
      // new one's version label.
      .then((notes) => {
        if (live) setState({ status: 'loaded', notes })
      })
      // A failed note fetch never blocks the refresh action, which lives in the footer and does not
      // depend on this state at all.
      .catch(() => {
        if (live) setState({ status: 'error' })
      })
    return () => {
      live = false
    }
  }, [open, selectedTag, attempt])

  const label = (b: BuildIdentity) => productVersionLabel(b.version)
  const notes = state.status === 'loaded' ? state.notes : null
  const titleVersion = productVersionLabel(selectedTag)
  const showCurrent = !browseHistory && !!target && buildKey(target) !== buildKey(current)
  const historyVisible = browseHistory && history.length > 1
  const selectedHistoryItem = history.find((item) => item.tag === selectedTag)
  const maturity = selectedHistoryItem?.maturity ?? (notes?.tag === selectedTag ? notes.maturity : '')
  const maturityTag = maturity ? (
    <Tag color={maturity === 'release' ? 'green' : 'blue'} bordered={false}>
      {t(maturity === 'release' ? 'update.release' : 'update.beta')}
    </Tag>
  ) : null

  return (
    <Modal
      open={open}
      onCancel={onClose}
      title={<Space size={8}>{t('update.notesTitle', { version: titleVersion })}{maturityTag}</Space>}
      width={980}
      className="rp-run-analysis-modal rp-release-notes-modal"
      // Rendering under the application root avoids Ant Design's body scroll lock. In this app,
      // html/body/#root are all height:100%; locking body collapses the document to one viewport
      // and forces a long page to scroll to zero before the dialog appears.
      getContainer={false}
      closable
      keyboard
      mask={{ closable: true }}
      destroyOnHidden
      footer={
        <div className="rp-release-notes-footer">
          <span className="rp-release-notes-footer__external">
            {/* Only when there IS a release page. A diagnostic build has none, and inventing a link
                would send a reader to a 404 dressed as documentation. */}
            {notes?.url ? (
              <Button type="link" href={notes.url} target="_blank" rel="noopener noreferrer" icon={<ExportOutlined />}>
                {t('update.viewOnGithub')}
              </Button>
            ) : null}
          </span>
          <Space>
            <Button onClick={onClose}>{t('common.close')}</Button>
            {onRefresh && (
              <Button type="primary" loading={refreshing} onClick={onRefresh}>
                {t('update.refreshTo', { version: titleVersion })}
              </Button>
            )}
          </Space>
        </div>
      }
    >
      {reminder && (
        <Alert
          type="warning"
          showIcon
          // The one thing a reader needs to know before they click Refresh: anything they have typed
          // and not saved goes with the page. Nothing is refreshed for them.
          title={t('update.requiredWarning')}
          style={{ marginBottom: 12 }}
        />
      )}
      {showCurrent && (
        <Typography.Text type="secondary" className="rp-release-notes-current">
          {t('update.currentVersion', { version: label(current) })}
        </Typography.Text>
      )}
      {historyVisible && (
        <div className="rp-release-history-mobile">
          <Select
            aria-label={t('update.history')}
            value={selectedTag}
            onChange={setSelectedTag}
            options={history.map((item) => ({
              value: item.tag,
              label: `${productVersionLabel(item.tag)} · ${t(item.maturity === 'release' ? 'update.release' : 'update.beta')}`,
            }))}
          />
        </div>
      )}
      <div className={historyVisible ? 'rp-release-notes-layout' : undefined}>
        {historyVisible && (
          <nav className="rp-release-history" aria-label={t('update.history')}>
            <Typography.Text strong>{t('update.history')}</Typography.Text>
            {history.map((item) => (
              <Button
                key={item.tag}
                type={item.tag === selectedTag ? 'default' : 'text'}
                block
                onClick={() => setSelectedTag(item.tag)}
              >
                <span>
                  <span className="rp-release-history__version">
                    <strong>{productVersionLabel(item.tag)}</strong>
                    <Tag color={item.maturity === 'release' ? 'green' : 'blue'} bordered={false}>
                      {t(item.maturity === 'release' ? 'update.release' : 'update.beta')}
                    </Tag>
                  </span>
                  {item.title && <small>{item.title}</small>}
                </span>
              </Button>
            ))}
          </nav>
        )}
        <div className="rp-release-notes-body">
        {state.status === 'loading' && (
          <div className="rp-release-notes-state">
            <Spin />
          </div>
        )}
        {state.status === 'error' && (
          <Alert
            type="error"
            showIcon
            title={t('update.notesError')}
            action={
              <Button size="small" onClick={() => setAttempt((n) => n + 1)}>
                {t('common.retry')}
              </Button>
            }
          />
        )}
        {state.status === 'loaded' && notes && !notes.available && (
          // An empty body would read as "this release changed nothing", which is a claim about the
          // release rather than about what this build packaged.
          <Alert
            type="info"
            showIcon
            title={t('update.notesUnavailable')}
            description={
              notes.url ? (
                <Typography.Link href={notes.url} target="_blank" rel="noopener noreferrer">
                  {notes.url}
                </Typography.Link>
              ) : undefined
            }
          />
        )}
        {state.status === 'loaded' && notes?.available && <Markdown md={notes.markdown} />}
        </div>
      </div>
    </Modal>
  )
}
