import { useEffect, useMemo, useRef, useState, type CSSProperties, type TransitionEvent } from 'react'
import { Button, Space, Modal, Typography, theme } from 'antd'
import {
  CheckCircleFilled,
  CloseCircleFilled,
  CloseOutlined,
  DownOutlined,
  ExclamationCircleFilled,
  InfoCircleFilled,
  UpOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router'
import { useAuth } from '../auth'
import { inScope, useAnnouncements } from '../announcements'
import {
  announcementSig,
  dismissBanner,
  dismissPopup,
  loadDismissals,
  markSeen,
  type DismissalState,
} from '../lib/announcementDismissal'
import type { Announcement, AnnouncementLevel } from '../api/types'

const LEVEL_ICON: Record<AnnouncementLevel, typeof InfoCircleFilled> = {
  notice: InfoCircleFilled,
  success: CheckCircleFilled,
  warning: ExclamationCircleFilled,
  error: CloseCircleFilled,
}

// antd token names per level. An announcement is painted entirely from ITS OWN level — background,
// icon, close button and body text. The band around it is neutral, so a blue notice can never land on
// the loudest row's amber; that mismatch (band tinted by the loudest, icon by its own level) is what
// made a mixed-level strip read as if the icon had been dropped onto the wrong row.
const LEVEL_TOKENS: Record<AnnouncementLevel, { bg: string; fg: string; text: string }> = {
  notice: { bg: 'colorInfoBg', fg: 'colorInfo', text: 'colorInfoText' },
  success: { bg: 'colorSuccessBg', fg: 'colorSuccess', text: 'colorSuccessText' },
  warning: { bg: 'colorWarningBg', fg: 'colorWarning', text: 'colorWarningText' },
  error: { bg: 'colorErrorBg', fg: 'colorError', text: 'colorErrorText' },
}

/**
 * One announcement, as a self-contained block: its own level colour, icon, optional close. This is
 * the single rendering every surface uses — the strip and both admin previews — so the console can
 * never show an operator something a reader will not see.
 */
export function AnnouncementRow({
  announcement,
  onClose,
  style,
}: {
  announcement: Pick<Announcement, 'level' | 'title' | 'content' | 'dismissible'>
  onClose?: () => void
  style?: CSSProperties
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const palette = token as unknown as Record<string, string>
  const Icon = LEVEL_ICON[announcement.level]
  const tokens = LEVEL_TOKENS[announcement.level]
  const title = announcement.title.trim()
  const content = announcement.content.trim()
  return (
    <div
      className="rp-announce-item"
      style={{
        // align-items:center puts the icon and the close button against the CENTRE of the
        // title+body block, instead of the close floating up at the first line.
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '7px 10px',
        borderRadius: 8,
        background: palette[tokens.bg],
        ...style,
      }}
    >
      <Icon style={{ color: palette[tokens.fg], fontSize: 15, flexShrink: 0 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        {title && (
          <div style={{ color: token.colorText, fontSize: 14, fontWeight: content ? 600 : 400 }}>{title}</div>
        )}
        {content && (
          <Typography.Text style={{ color: palette[tokens.text], whiteSpace: 'pre-line', lineHeight: 1.5 }}>
            {content}
          </Typography.Text>
        )}
      </div>
      {announcement.dismissible && onClose && (
        <Button
          type="text"
          size="small"
          aria-label={t('announcement.close')}
          icon={<CloseOutlined />}
          onClick={onClose}
          style={{ flexShrink: 0, color: palette[tokens.fg], opacity: 0.75 }}
        />
      )}
    </div>
  )
}

// The fold animation. The overflow stays mounted while it collapses so the grid can run 1fr→0fr,
// then unmounts once the transition ends; expansion mounts it first and lets the grid run 0fr→1fr. A
// grid row transition animates height without measuring the content.
function useFold(expanded: boolean) {
  const [mounted, setMounted] = useState(expanded)
  useEffect(() => {
    if (expanded) setMounted(true)
  }, [expanded])
  const onEnd = (e: TransitionEvent) => {
    if (e.propertyName !== 'grid-template-rows') return
    if (!expanded) setMounted(false)
  }
  return { mounted, onEnd }
}

// Shared plumbing: the reader's dismissal state for whatever set of announcements is on offer, and
// the local record of what they have closed in this render.
function useReaderState(items: Announcement[]) {
  const { user } = useAuth()
  const [closed, setClosed] = useState<Record<number, boolean>>({})
  // The payload's identity: what is on offer AND what each one says. It is what the memo below
  // depends on, so a navigation alone never rebuilds this.
  const identity = items.map((a) => `${a.id}:${announcementSig(a)}`).join(',')
  const dismissals = useMemo<DismissalState>(
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity IS the dependency on `items`
    () => loadDismissals(user || '', items),
    [user, identity],
  )
  const open = items.filter((a) => !closed[a.id] && !dismissals.bannerDismissed(a))
  const close = (a: Announcement) => {
    dismissBanner(user || '', a)
    setClosed((prev) => ({ ...prev, [a.id]: true }))
  }
  return { open, close, dismissals, identity, user: user || '' }
}

/**
 * The announcement strip: every announcement in scope — app-scoped everywhere, home-scoped on the
 * home page — as one list in the operator's order, on a neutral band under the header.
 *
 * One self-contained block per announcement, each painted with its own level's colour. This band
 * follows the reader onto every page, so folding is how it stays chrome-sized: the lead stays, the
 * rest fold behind a "N more" toggle that expands in place with a height transition.
 */
export function AnnouncementStrip({ compact = false, maxWidth }: { compact?: boolean; maxWidth?: number | string }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const loc = useLocation()
  const { items, collapse } = useAnnouncements()
  const [expanded, setExpanded] = useState(false)
  const mine = useMemo(() => inScope(items, loc.pathname), [items, loc.pathname])
  const { open, close } = useReaderState(mine)
  const { mounted, onEnd } = useFold(!collapse || expanded)

  if (!open.length) return null
  // Folded is opt-in and off by default. Folding hides the OVERFLOW, not the lead: the first
  // announcement keeps its title AND body, and only the ones after it collapse behind the "N more"
  // link. An earlier version truncated the lead to one bare line, which hid the very thing the
  // operator wanted read; folding is about how many messages show, not how much of one shows.
  const [lead, ...rest] = open
  const revealed = !collapse || expanded

  return (
    <div className="rp-announce-strip">
      <div
        style={{
          maxWidth,
          margin: '0 auto',
          padding: compact ? '8px 12px' : '8px 20px',
          display: 'flex',
          flexDirection: 'column',
        }}
      >
        <AnnouncementRow announcement={lead} onClose={() => close(lead)} />
        {rest.length > 0 && (
          <>
            <div
              aria-hidden={collapse && !expanded ? true : undefined}
              onTransitionEnd={collapse ? onEnd : undefined}
              style={{
                display: 'grid',
                gridTemplateRows: revealed ? '1fr' : '0fr',
                transition: collapse ? 'grid-template-rows 220ms ease' : undefined,
              }}
            >
              <div style={{ overflow: collapse ? 'hidden' : undefined, minHeight: 0 }}>
                {mounted && (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 6 }}>
                    {rest.map((a) => (
                      <AnnouncementRow key={a.id} announcement={a} onClose={() => close(a)} />
                    ))}
                  </div>
                )}
              </div>
            </div>
            {collapse && (
              <button
                type="button"
                aria-expanded={expanded}
                onClick={() => setExpanded((v) => !v)}
                style={{
                  alignSelf: 'flex-start',
                  marginLeft: 25,
                  marginTop: 6,
                  background: 'none',
                  border: 0,
                  padding: 0,
                  cursor: 'pointer',
                  color: token.colorTextSecondary,
                  font: 'inherit',
                  fontSize: 12,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                }}
              >
                {expanded ? t('announcement.showLess') : t('announcement.showMore', { count: rest.length })}
                {expanded ? <UpOutlined style={{ fontSize: 10 }} /> : <DownOutlined style={{ fontSize: 10 }} />}
              </button>
            )}
          </>
        )}
      </div>
    </div>
  )
}

/**
 * The announcement popup: every eligible announcement in one dialog, a list of their titles down the
 * left and the selected one's full text on the right — the release-notes dialog's shape, because that
 * is the portal's one idea of a dialog that can hold several things a reader picks between.
 *
 * One dialog, not one per announcement. Firing them in turn would make the reader dismiss two modals
 * in one sitting, and the footer acts on the WHOLE dialog: "Got it" silences all of them for this
 * session, "don't show again" silences all for good. A reader who wants one to keep interrupting can
 * still find it in the strip.
 *
 * It offers the same in-scope set the strip draws, so a row shown there is the row offered here.
 */
export function AnnouncementPopup() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const loc = useLocation()
  const { items } = useAnnouncements()
  const visible = useMemo(() => inScope(items, loc.pathname), [items, loc.pathname])
  const { dismissals, identity, user } = useReaderState(visible)
  // Offer the whole batch once per signature set, then never again in this tab: [identity] is what
  // re-arms it, and closing the dialog does not change identity, so dismissal sticks.
  const offered = useRef('')

  const eligible = useMemo(
    () =>
      visible.filter(
        (a) => a.popup && !dismissals.popupDismissed(a) && !dismissals.seenThisSession(a),
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity IS the dependency on `visible`
    [identity, dismissals],
  )
  const [open, setOpen] = useState(false)
  const [activeId, setActiveId] = useState<number | null>(null)

  useEffect(() => {
    if (!eligible.length) return
    const key = eligible.map((a) => `${a.id}:${announcementSig(a)}`).join(',')
    if (offered.current === key) return
    offered.current = key
    setActiveId(eligible[0].id)
    setOpen(true)
  }, [eligible])

  // "Got it" stops the popup for this session; "don't show again" stops it for good. Both act on
  // EVERY announcement the dialog is showing — it is one interruption, so it takes one answer.
  const closePopup = (forGood: boolean) => {
    for (const a of eligible) {
      if (forGood) dismissPopup(user, a)
      else markSeen(user, a)
    }
    setOpen(false)
  }

  const active = eligible.find((a) => a.id === activeId) ?? eligible[0]
  const accent = active ? (token as unknown as Record<string, string>)[LEVEL_TOKENS[active.level].fg] : token.colorPrimary
  return (
    <Modal
      rootClassName="rp-announce-popup rp-release-notes-modal"
      open={open && !!eligible.length}
      width={880}
      title={
        <Space size={8}>
          <InfoCircleFilled style={{ color: accent }} />
          {t('announcement.popupTitle')}
        </Space>
      }
      // No built-in corner X: the footer already offers "Got it" (this session) and "Don't show
      // again" (for good), and a third exit that means one of those two is a worse choice, not a
      // shortcut.
      closable={false}
      onCancel={() => closePopup(false)}
      footer={
        <Space style={{ width: '100%', justifyContent: 'flex-end' }} wrap>
          <Button onClick={() => closePopup(true)}>{t('announcement.dontShowAgain')}</Button>
          <Button type="primary" onClick={() => closePopup(false)}>
            {t('announcement.gotIt')}
          </Button>
        </Space>
      }
      destroyOnHidden
    >
      {/* Two panes once there is something to pick between; a single announcement renders as one
          row, the same block the strip shows. */}
      <div className={eligible.length > 1 ? 'rp-announce-popup-layout' : undefined}>
        {eligible.length > 1 && (
          <nav className="rp-announce-popup-list" aria-label={t('announcement.popupTitle')}>
            {eligible.map((a) => {
              const Icon = LEVEL_ICON[a.level]
              return (
                <Button
                  key={a.id}
                  type={a.id === active?.id ? 'default' : 'text'}
                  block
                  onClick={() => setActiveId(a.id)}
                >
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8, width: '100%' }}>
                    <Icon style={{ color: (token as unknown as Record<string, string>)[LEVEL_TOKENS[a.level].fg] }} />
                    <span
                      style={{
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        textAlign: 'start',
                      }}
                    >
                      {a.title || a.content.split('\n')[0]}
                    </span>
                  </span>
                </Button>
              )
            })}
          </nav>
        )}
        <div className="rp-announce-popup-body">
          {active && <AnnouncementRow announcement={active} />}
        </div>
      </div>
    </Modal>
  )
}
