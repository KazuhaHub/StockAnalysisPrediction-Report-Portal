import { useEffect, useRef, useState } from 'react'
import { Button, Empty, Input, Modal, Typography } from 'antd'
import type { InputRef } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { searchAdminPages } from './adminNavigation'

export default function AdminSearch({ open, onClose, onNavigate }: {
  open: boolean
  onClose: () => void
  onNavigate: (path: string) => void
}) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const input = useRef<InputRef>(null)
  const results = searchAdminPages(query, t)
  useEffect(() => { if (!open) setQuery('') }, [open])

  const choose = (key: string) => { onNavigate(`/manage/${key}`); onClose() }
  return (
    <Modal title={t('manage.search')} open={open} onCancel={onClose} footer={null}
      afterOpenChange={(visible) => { if (visible) input.current?.focus() }}>
      <Input ref={input} value={query} allowClear prefix={<SearchOutlined />}
        aria-label={t('manage.search')} placeholder={t('manage.searchPlaceholder')}
        onChange={(event) => setQuery(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown') {
            event.preventDefault()
            document.getElementById('rp-admin-search-results')?.querySelector('button')?.focus()
          }
          if (event.key === 'Enter' && results.length === 1) choose(results[0].key)
        }} />
      <div id="rp-admin-search-results" className="rp-admin-search-results" aria-live="polite">
        {results.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('manage.noResults')} /> : results.map((page) => (
          <Button key={page.key} type="text" className="rp-admin-search-result" icon={page.icon} onClick={() => choose(page.key)}>
            <span><Typography.Text strong>{t(page.label)}</Typography.Text>
              <Typography.Text type="secondary">{t(`manage.description.${page.key}`)}</Typography.Text></span>
          </Button>
        ))}
      </div>
    </Modal>
  )
}
