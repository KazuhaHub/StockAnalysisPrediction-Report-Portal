import { Card, Typography } from 'antd'
import { ArrowRightOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ADMIN_GROUPS } from './adminNavigation'

export default function AdminOverviewPage() {
  const { t } = useTranslation()
  return (
    <div className="rp-admin-overview">
      {ADMIN_GROUPS.map((group) => (
        <Card key={group.key} size="small" title={t(group.label)}>
          <div className="rp-admin-overview__pages">
            {group.pages.map((page) => (
              <Link key={page.key} to={`/manage/${page.key}`} className="rp-admin-overview__link">
                <span className="rp-admin-overview__icon">{page.icon}</span>
                <span><Typography.Text strong>{t(page.label)}</Typography.Text>
                  <Typography.Text type="secondary">{t(`manage.description.${page.key}`)}</Typography.Text></span>
                <ArrowRightOutlined />
              </Link>
            ))}
          </div>
        </Card>
      ))}
    </div>
  )
}
