import { Card, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

export default function ForecastMethodology() {
  const { t } = useTranslation()
  return (
    <Card title={t('forecast.methodology.title')}>
      <Typography.Paragraph>{t('forecast.methodology.population')}</Typography.Paragraph>
      <Typography.Paragraph>{t('forecast.methodology.gates')}</Typography.Paragraph>
      <Typography.Paragraph>{t('forecast.methodology.statistics')}</Typography.Paragraph>
      <Typography.Paragraph>{t('forecast.methodology.sources')}</Typography.Paragraph>
      <Typography.Paragraph style={{ marginBottom: 0 }}>{t('forecast.disclaimer')}</Typography.Paragraph>
    </Card>
  )
}
