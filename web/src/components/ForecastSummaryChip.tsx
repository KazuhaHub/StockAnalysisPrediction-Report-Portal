import { Skeleton, Tag, Tooltip, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { ForecastReadingSummary } from '../api/types'

interface Props {
  prediction?: ForecastReadingSummary
  loading?: boolean
}

export default function ForecastSummaryChip({ prediction, loading = false }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()

  if (loading) {
    return <div style={{ height: 28 }}><Skeleton.Button active size="small" style={{ width: 168, height: 22 }} data-testid="forecast-chip-loading" /></div>
  }
  if (!prediction) return <div aria-hidden="true" style={{ height: 28 }} />

  const chip = prediction.has_prediction ? (
    <a href="#forecast"><Tag style={{ color: token.colorText, background: prediction.stance === 'long' ? token.colorInfoBg : undefined }} data-testid="forecast-summary-chip">
      {t(`forecast.stance.${prediction.stance}`)}
      {prediction.weight_bp !== undefined ? ` · ${(prediction.weight_bp / 100).toFixed(0)}%` : ''}
      {prediction.upside_bp !== undefined ? ` · ${prediction.upside_bp >= 0 ? '+' : ''}${(prediction.upside_bp / 100).toFixed(1)}%` : ''}
    </Tag></a>
  ) : (
    <Link to={prediction.sibling.url}>
      <Tag data-testid="forecast-summary-chip">{t('forecast.reading.sibling_chip')}</Tag>
    </Link>
  )

  return <div style={{ minHeight: 28 }}><Tooltip title={t('forecast.disclaimer')}>{chip}</Tooltip></div>
}
