import { Suspense, useCallback, useEffect, useState } from 'react'
import { Button, ConfigProvider, Drawer, Menu, Space, Spin, Typography, theme } from 'antd'
import type { MenuProps } from 'antd'
import { HomeOutlined, MenuFoldOutlined, MenuUnfoldOutlined, SearchOutlined } from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { prefetch } from '../../lib/prefetch'
import VersionLabel from '../../components/VersionLabel'
import { useSite } from '../../site'
import AdminSearch from './AdminSearch'
import { ADMIN_GROUPS, adminPage } from './adminNavigation'

const COLLAPSE_KEY = 'rp.manage.sider.collapsed'
const OPEN_KEY = 'rp.manage.sider.open'
const NARROW_QUERY = '(max-width: 767px)'
const HEADER_VAR = 'var(--rp-header-h, 64px)'

export default function ManageLayout() {
  const { t } = useTranslation()
  const { settings } = useSite()
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
  const nested = loc.pathname.split('/').filter(Boolean).length > 2
  const group = ADMIN_GROUPS.find((entry) => entry.pages.some((item) => item.key === active))
  const activeLabel = page ? t(page.label) : t('manage.overview')
  const [openKeys, setOpenKeys] = useState<string[]>(() => {
    if (group) return [`group.${group.key}`]
    try {
      const stored: unknown = JSON.parse(localStorage.getItem(OPEN_KEY) || 'null')
      if (Array.isArray(stored)) return stored.filter((key): key is string => ADMIN_GROUPS.some(section => `group.${section.key}` === key)).slice(-1)
    } catch { /* Storage is optional. */ }
    return []
  })
  const rememberOpen = useCallback((keys: string[]) => {
    setOpenKeys(keys)
    try { localStorage.setItem(OPEN_KEY, JSON.stringify(keys)) } catch { /* Storage is optional. */ }
  }, [])
  useEffect(() => {
    if (group) {
      rememberOpen([`group.${group.key}`])
    }
  }, [group, rememberOpen])

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
      key: `group.${section.key}`,
      label: t(section.label),
      icon: section.pages[0].icon,
      children: section.pages.map((entry) => ({
        key: entry.key, label: t(entry.label), icon: entry.icon,
        // Only cheap page-defining requests belong here, never statistics or market fetches.
        onMouseEnter: () => { if (entry.warm) void prefetch(entry.warm) },
      })),
    })),
  ]
  const menu = <ConfigProvider theme={{ components: { Menu: {
    itemBg: 'transparent', subMenuItemBg: 'transparent', popupBg: token.colorBgContainer,
    itemBorderRadius: 8,
    itemSelectedBg: token.colorFillSecondary, itemSelectedColor: token.colorText,
    subMenuItemSelectedColor: token.colorText, itemHoverBg: token.colorFillQuaternary,
  } } }}><Menu className="rp-admin-menu" mode="inline" inlineCollapsed={railCollapsed} selectedKeys={[active]}
    openKeys={railCollapsed ? undefined : openKeys} onOpenChange={(keys) => {
      const opened = keys.find(key => !openKeys.includes(key))
      rememberOpen(opened ? [opened] : keys)
    }}
    items={menuItems} onClick={({ key }) => { if (key === 'overview') rememberOpen([]); go(`/manage/${key}`) }}
    style={{ border: 'none', background: 'transparent' }} /></ConfigProvider>

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
        {/* A page nested under a page (/manage/apps/:key) has its own breadcrumb; repeating the list's description above it adds nothing. */}
        {!nested && (
          <Typography.Paragraph type="secondary">
            {t(page ? `manage.description.${page.key}` : 'manage.overviewDescription')}
          </Typography.Paragraph>
        )}
      </div>
      {!page && <div id="rp-admin-page-actions" className="rp-admin-page-actions" />}
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
    <div className={`rp-admin-content${active === 'users' ? ' rp-admin-content--directory' : ''}`} id="rp-admin-content">
      {header}
      <div className="rp-admin-page-body" key={loc.pathname}>
      <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
        <Outlet />
      </Suspense>
      </div>
    </div>
  )

  return (
    <div className={`rp-admin-shell${narrow ? ' rp-admin-shell--narrow' : ''}`}
      style={{ height: `calc(100dvh - ${HEADER_VAR})`, background: token.colorBgContainer }}>
      <AdminSearch open={searchOpen} onClose={() => setSearchOpen(false)} onNavigate={go} />
      {narrow ? (
        <>
          <div className="rp-admin-mobile-bar" style={{ background: token.colorBgContainer }}>
            <Button icon={<MenuUnfoldOutlined />} aria-label={t('nav.manage')} onClick={() => setDrawerOpen(true)}>{activeLabel}</Button>
            {searchButton}
          </div>
          <Drawer open={drawerOpen} onClose={() => setDrawerOpen(false)} placement="left" size={280}
            title={t('nav.manage')} styles={{ body: { padding: 8 } }}>{menu}</Drawer>
        </>
      ) : (
        <aside className="rp-admin-rail" aria-label={t('nav.manage')}
          style={{ flexBasis: collapsed ? 80 : 236, width: collapsed ? 80 : 236,
            height: `calc(100dvh - ${HEADER_VAR})` }}>
          <div className="rp-admin-rail__search">{searchButton}</div>
          <div className="rp-admin-rail__menu">{menu}</div>
          <div className="rp-admin-rail__footer" style={{ justifyContent: collapsed ? 'center' : 'space-between' }}>
            <Button type="text" aria-label={t('nav.collapse')} onClick={toggle}
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}>
              {!collapsed && t('nav.collapse')}
            </Button>
            {!collapsed && settings.versionDisplay === 'footer' && <VersionLabel />}
          </div>
        </aside>
      )}
      {content}
    </div>
  )
}
