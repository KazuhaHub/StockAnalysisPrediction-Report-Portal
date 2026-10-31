import { Card, Collapse, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

// The method is reference reading: folded by default so it does not push the results down, while the
// disclaimer stays in view, since it qualifies everything on the page.
export default function ForecastMethodology() {
  const { t } = useTranslation()
  return (
    <Card title={t('forecast.methodology.title')}>
      <Collapse ghost size="small" style={{ marginInline: -12, marginBlockStart: -8 }} items={[{
        key: 'method',
        label: t('forecast.methodology.read'),
        children: <>
          <Typography.Paragraph>{t('forecast.methodology.population')}</Typography.Paragraph>
          <Typography.Paragraph>{t('forecast.methodology.gates')}</Typography.Paragraph>
          <Typography.Paragraph>{t('forecast.methodology.statistics')}</Typography.Paragraph>
          <Typography.Paragraph style={{ marginBottom: 0 }}>{t('forecast.methodology.sources')}</Typography.Paragraph>
        </>,
      }]} />
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0, marginTop: 8 }}>{t('forecast.disclaimer')}</Typography.Paragraph>
    </Card>
  )
}
