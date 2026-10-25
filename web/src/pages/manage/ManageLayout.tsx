import { Suspense, useEffect, useState } from 'react'
import { Button, Drawer, Menu, Space, Spin, Typography, theme } from 'antd'
import type { MenuProps } from 'antd'
import { HomeOutlined, MenuFoldOutlined, MenuUnfoldOutlined, SearchOutlined } from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { prefetch } from '../../lib/prefetch'
import VersionLabel from '../../components/VersionLabel'
import AdminSearch from './AdminSearch'
import { ADMIN_GROUPS, adminPage } from './adminNavigation'

const COLLAPSE_KEY = 'rp.manage.sider.collapsed'
const NARROW_QUERY = '(max-width: 767px)'
const HEADER_VAR = 'var(--rp-header-h, 64px)'

export default function ManageLayout() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const loc = useLocation()
  const { token } = theme.useToken()
  const [collapsed, setCollapsed] = useState(() => {
    try { return localStorage.getItem(COLLAPSE_KEY) === '1' } catch { return false }
  })
  const [narrow, setNarrow] = useState(() => window.matchMedia(NARROW_QUERY).matches)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const active = loc.pathname.split('/')[2] || 'overview'
  const page = adminPage(active)
  const group = ADMIN_GROUPS.find((entry) => entry.pages.some((item) => item.key === active))
  const activeLabel = page ? t(page.label) : t('manage.overview')

  useEffect(() => {
    const mq = window.matchMedia(NARROW_QUERY)
    const onChange = () => setNarrow(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  const toggle = () => setCollapsed((current) => {
    const next = !current
    try { localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0') } catch { /* Storage is optional. */ }
    return next
  })
  const go = (path: string) => { navigate(path); setDrawerOpen(false) }
  const railCollapsed = !narrow && collapsed
  const menuItems: MenuProps['items'] = [
    { key: 'overview', label: t('manage.overview'), icon: <HomeOutlined /> },
    ...ADMIN_GROUPS.map((section) => ({
      type: 'group' as const,
      key: section.key,
      label: t(section.label),
      children: section.pages.map((entry) => ({
        key: entry.key, label: t(entry.label), icon: entry.icon,
        // Only cheap page-defining requests belong here, never statistics or market fetches.
        onMouseEnter: () => { if (entry.warm) void prefetch(entry.warm) },
      })),
    })),
  ]
  const menu = <Menu mode="inline" inlineCollapsed={railCollapsed} selectedKeys={[active]}
    items={menuItems} onClick={({ key }) => go(`/manage/${key}`)}
    style={{ border: 'none', background: 'transparent' }} />

  const searchButton = (
    <Button icon={<SearchOutlined />} aria-label={t('manage.search')} onClick={() => setSearchOpen(true)}
      block={!narrow} type={railCollapsed ? 'text' : 'default'}>
      {!railCollapsed && t('manage.search')}
    </Button>
  )
  const header = (
    <header className="rp-admin-page-header">
      <div>
        <Typography.Text type="secondary" className="rp-admin-page-header__area">
          {group ? t(group.label) : t('nav.manage')}
        </Typography.Text>
        <Typography.Title level={2}>{activeLabel}</Typography.Title>
        <Typography.Paragraph type="secondary">
          {t(page ? `manage.description.${page.key}` : 'manage.overviewDescription')}
        </Typography.Paragraph>
      </div>
      {page && (
        <nav aria-label={t('manage.related')} className="rp-admin-related">
          <Typography.Text type="secondary">{t('manage.related')}</Typography.Text>
          <Space wrap size={[4, 4]}>
            {page.related.map((key) => {
              const related = adminPage(key)
              return related && <Button key={key} size="small" onClick={() => go(`/manage/${key}`)}>{t(related.label)}</Button>
            })}
          </Space>
        </nav>
      )}
    </header>
  )
  const content = (
    <div className="rp-admin-content" id="rp-admin-content">
      {header}
      <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
        <Outlet />
      </Suspense>
    </div>
  )

  return (
    <div className={`rp-admin-shell${narrow ? ' rp-admin-shell--narrow' : ''}`}
      style={{ minHeight: `calc(100dvh - ${HEADER_VAR})`, background: token.colorBgContainer }}>
      <AdminSearch open={searchOpen} onClose={() => setSearchOpen(false)} onNavigate={go} />
      {narrow ? (
        <>
          <div className="rp-admin-mobile-bar" style={{ top: HEADER_VAR, background: token.colorBgContainer }}>
            <Button icon={<MenuUnfoldOutlined />} aria-label={t('nav.manage')} onClick={() => setDrawerOpen(true)}>{activeLabel}</Button>
            {searchButton}
          </div>
          <Drawer open={drawerOpen} onClose={() => setDrawerOpen(false)} placement="left" size={280}
            title={t('nav.manage')} styles={{ body: { padding: 8 } }}>{menu}</Drawer>
        </>
      ) : (
        <aside className="rp-admin-rail" aria-label={t('nav.manage')}
          style={{ flexBasis: collapsed ? 80 : 236, width: collapsed ? 80 : 236,
            top: HEADER_VAR, height: `calc(100dvh - ${HEADER_VAR})` }}>
          <div className="rp-admin-rail__search">{searchButton}</div>
          <div className="rp-admin-rail__menu">{menu}</div>
          <div className="rp-admin-rail__footer" style={{ justifyContent: collapsed ? 'center' : 'space-between' }}>
            <Button type="text" aria-label={t('nav.collapse')} onClick={toggle}
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}>
              {!collapsed && t('nav.collapse')}
            </Button>
            {!collapsed && <VersionLabel />}
          </div>
        </aside>
      )}
      {content}
    </div>
  )
}
