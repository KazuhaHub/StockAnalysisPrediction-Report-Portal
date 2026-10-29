import type { ReactNode } from 'react'
import {
  ApiOutlined, AppstoreAddOutlined, AuditOutlined, BarChartOutlined, BranchesOutlined, ControlOutlined,
  DatabaseOutlined, ExportOutlined, FileTextOutlined, FundProjectionScreenOutlined, GlobalOutlined,
  LoginOutlined, KeyOutlined, LineChartOutlined, LinkOutlined, LockOutlined, MailOutlined,
  MessageOutlined, NotificationOutlined, PlayCircleOutlined, SlidersOutlined, TagsOutlined, TeamOutlined,
} from '@ant-design/icons'

export interface AdminPage {
  key: string
  label: string
  icon: ReactNode
  warm?: string
  related: string[]
}

export const ADMIN_GROUPS: { key: string; label: string; pages: AdminPage[] }[] = [
  { key: 'site', label: 'nav.group.site', pages: [
    { key: 'site', label: 'settings.general', icon: <GlobalOutlined />, warm: '/api/admin/settings', related: ['email', 'security'] },
    { key: 'announcement', label: 'nav.announcement', icon: <NotificationOutlined />, warm: '/api/admin/announcements', related: ['users', 'links'] },
    { key: 'email', label: 'nav.email', icon: <MailOutlined />, warm: '/api/admin/email', related: ['notifications', 'security', 'site'] },
    { key: 'links', label: 'nav.links', icon: <LinkOutlined />, warm: '/api/admin/links', related: ['apps', 'announcement'] },
  ] },
  { key: 'content', label: 'nav.group.content', pages: [
    { key: 'types', label: 'nav.types', icon: <TagsOutlined />, warm: '/api/admin/types', related: ['versions', 'batch'] },
    { key: 'versions', label: 'nav.versions', icon: <BranchesOutlined />, warm: '/api/admin/versions', related: ['users', 'types'] },
    { key: 'forecast', label: 'nav.forecast', icon: <FundProjectionScreenOutlined />, warm: '/api/admin/forecast', related: ['quotes', 'users', 'audit'] },
  ] },
  { key: 'access', label: 'nav.group.access', pages: [
    { key: 'users', label: 'nav.users', icon: <TeamOutlined />, warm: '/api/admin/users', related: ['sso', 'security', 'versions'] },
    { key: 'sso', label: 'nav.sso', icon: <LoginOutlined />, warm: '/api/admin/sso/providers', related: ['users', 'security', 'site'] },
    { key: 'security', label: 'nav.security', icon: <LockOutlined />, warm: '/api/admin/security', related: ['sso', 'users', 'email'] },
    { key: 'tokens', label: 'settings.tokens', icon: <KeyOutlined />, warm: '/api/admin/tokens', related: ['apidoc', 'notifications', 'audit'] },
  ] },
  { key: 'batch', label: 'nav.group.batch', pages: [
    { key: 'batch', label: 'nav.batchAdmin', icon: <PlayCircleOutlined />, warm: '/api/admin/batch/targets', related: ['runqueue', 'rundefaults', 'users'] },
    { key: 'runqueue', label: 'manage.label.runqueue', icon: <ControlOutlined />, warm: '/api/admin/batch/config', related: ['users', 'rundefaults', 'batch'] },
    { key: 'rundefaults', label: 'nav.runDefaults', icon: <SlidersOutlined />, warm: '/api/admin/batch/config', related: ['batch', 'runqueue', 'notifications'] },
    { key: 'assistant', label: 'manage.label.assistant', icon: <MessageOutlined />, related: ['batch', 'users'] },
  ] },
  { key: 'integrations', label: 'nav.group.integrations', pages: [
    { key: 'apps', label: 'nav.appsAdmin', icon: <AppstoreAddOutlined />, related: ['links', 'tokens', 'apidoc'] },
    { key: 'webhooks', label: 'nav.webhooks', icon: <ApiOutlined />, warm: '/api/admin/webhooks', related: ['notifications', 'apidoc'] },
    { key: 'notifications', label: 'nav.notifications', icon: <NotificationOutlined />, related: ['email', 'webhooks', 'tokens'] },
    { key: 'apidoc', label: 'settings.apidoc', icon: <FileTextOutlined />, related: ['tokens', 'webhooks', 'apps'] },
  ] },
  { key: 'system', label: 'nav.group.system', pages: [
    { key: 'statistics', label: 'statistics.title', icon: <BarChartOutlined />, related: ['types', 'runqueue', 'users'] },
    { key: 'exports', label: 'nav.export', icon: <ExportOutlined />, related: ['statistics', 'types', 'storage'] },
    { key: 'quotes', label: 'nav.quoteAdmin', icon: <LineChartOutlined />, related: ['forecast', 'storage'] },
    { key: 'storage', label: 'nav.storage', icon: <DatabaseOutlined />, warm: '/api/admin/cleanup/config', related: ['notifications', 'audit', 'forecast'] },
    { key: 'audit', label: 'nav.audit', icon: <AuditOutlined />, related: ['security', 'storage'] },
  ] },
]

export const ADMIN_PAGES = ADMIN_GROUPS.flatMap((group) => group.pages)
export const adminPage = (key: string) => ADMIN_PAGES.find((page) => page.key === key)

/** Match translated task topics as well as page names; all query words must match. */
export function searchAdminPages(query: string, translate: (key: string) => string): AdminPage[] {
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  return ADMIN_GROUPS.flatMap((group) => group.pages.filter((page) => {
    const content = [translate(group.label), translate(page.label), translate(`manage.description.${page.key}`), page.key]
      .join(' ').toLocaleLowerCase()
    return terms.every((term) => content.includes(term))
  }))
}
