import { useEffect, useState } from 'react'
import { Alert, Button, Modal, Space, Spin, Typography } from 'antd'
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
// It is also the surface a `required` policy escalates to. That case is the SAME dialog with its
// exits removed, not a second, blocking prompt stacked on top: two overlays fighting over the same
// page is how a reader ends up with one they cannot read and one they cannot close.

export type ReleaseNotes = { tag: string; available: boolean; markdown: string; url: string }

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
  const required = policy === 'required'
  const tag = target?.version ?? ''
  const targetKey = target ? buildKey(target) : ''

  useEffect(() => {
    if (!open || !tag) return
    let live = true
    setState({ status: 'loading' })
    // The tag is passed through the query string, not interpolated into a path: it is compared
    // against the server's own tag server-side, and a value that is not a release tag simply has no
    // note to serve.
    api
      .get<ReleaseNotes>(`/api/release-notes?tag=${encodeURIComponent(tag)}`)
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
  }, [open, tag, targetKey, attempt])

  const label = (b: BuildIdentity) => productVersionLabel(b.version)
  const notes = state.status === 'loaded' ? state.notes : null
  const titleVersion = target ? label(target) : ''
  const showCurrent = !!target && buildKey(target) !== buildKey(current)

  return (
    <Modal
      open={open}
      onCancel={required ? undefined : onClose}
      title={t('update.notesTitle', { version: titleVersion })}
      width={980}
      className="rp-run-analysis-modal rp-release-notes-modal"
      // Required mode: no X, no Escape, no mask dismissal. antd draws the X from `closable`, and
      // `keyboard`/`mask.closable` are the other two ways out; all three have to go together or the
      // "must refresh" policy is only a suggestion.
      closable={!required}
      keyboard={!required}
      mask={{ closable: !required }}
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
            {!required && (
              <Button onClick={onClose}>{t('common.close')}</Button>
            )}
            {onRefresh && (
              <Button type="primary" loading={refreshing} onClick={onRefresh}>
                {t('update.refreshTo', { version: titleVersion })}
              </Button>
            )}
          </Space>
        </div>
      }
    >
      {required && (
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
    </Modal>
  )
}
